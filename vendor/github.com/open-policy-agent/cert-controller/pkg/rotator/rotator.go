package rotator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/atomic"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	defaultControllerName             = "cert-rotator"
	defaultCertName                   = "tls.crt"
	defaultKeyName                    = "tls.key"
	caCertName                        = "ca.crt"
	caKeyName                         = "ca.key"
	defaultRotationCheckFrequency     = 12 * time.Hour
	defaultCaCertValidityDuration     = 10 * 365 * 24 * time.Hour
	defaultServerCertValidityDuration = 1 * 365 * 24 * time.Hour
	defaultLookaheadInterval          = 90 * 24 * time.Hour
)

var crLog = logf.Log.WithName("cert-rotation")

// WebhookType it the type of webhook, either validating/mutating webhook, a CRD conversion webhook, or an extension API server.
type WebhookType int

const (
	// Validating indicates the webhook is a ValidatingWebhook.
	Validating WebhookType = iota
	// Mutating indicates the webhook is a MutatingWebhook.
	Mutating
	// CRDConversion indicates the webhook is a conversion webhook.
	CRDConversion
	// APIService indicates the webhook is an extension API server.
	APIService
	// ExternalDataProvider indicates the webhook is a Gatekeeper External Data Provider.
	ExternalDataProvider
)

var (
	_ manager.Runnable               = &CertRotator{}
	_ manager.LeaderElectionRunnable = &CertRotator{}
	_ manager.Runnable               = controllerWrapper{}
	_ manager.LeaderElectionRunnable = controllerWrapper{}
	_ manager.Runnable               = &cacheWrapper{}
	_ manager.LeaderElectionRunnable = &cacheWrapper{}
)

type controllerWrapper struct {
	controller.Controller
	needLeaderElection bool
}

func (cw controllerWrapper) NeedLeaderElection() bool {
	return cw.needLeaderElection
}

type cacheWrapper struct {
	cache.Cache
	needLeaderElection bool
}

func (cw *cacheWrapper) NeedLeaderElection() bool {
	return cw.needLeaderElection
}

// WebhookInfo is used by the rotator to receive info about resources to be updated with certificates.
type WebhookInfo struct {
	// Name is the name of the webhook for a validating or mutating webhook, or the CRD name in case of a CRD conversion webhook
	Name string
	Type WebhookType
}

func (w WebhookInfo) gvk() schema.GroupVersionKind {
	t2g := map[WebhookType]schema.GroupVersionKind{
		Validating:           {Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"},
		Mutating:             {Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration"},
		CRDConversion:        {Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"},
		APIService:           {Group: "apiregistration.k8s.io", Version: "v1", Kind: "APIService"},
		ExternalDataProvider: {Group: "externaldata.gatekeeper.sh", Version: "v1beta1", Kind: "Provider"},
	}
	return t2g[w.Type]
}

// AddRotator adds the CertRotator and ReconcileWH to the manager.
func AddRotator(mgr manager.Manager, cr *CertRotator) error {
	if mgr == nil || cr == nil {
		return fmt.Errorf("nil arguments")
	}
	ns := cr.SecretKey.Namespace
	if ns == "" {
		return fmt.Errorf("invalid namespace for secret")
	}
	cache, err := addNamespacedCache(mgr, cr, ns)
	if err != nil {
		return fmt.Errorf("creating namespaced cache: %w", err)
	}

	cr.reader = cache
	cr.writer = mgr.GetClient() // TODO make overrideable
	cr.certsMounted = make(chan struct{})
	cr.certsNotMounted = make(chan struct{})
	cr.wasCAInjected = atomic.NewBool(false)
	cr.caNotInjected = make(chan struct{})
	if !cr.testNoBackgroundRotation {
		if err := mgr.Add(cr); err != nil {
			return err
		}
	}
	if cr.controllerName == "" {
		cr.controllerName = defaultControllerName
	}
	if cr.CertName == "" {
		cr.CertName = defaultCertName
	}

	if cr.KeyName == "" {
		cr.KeyName = defaultKeyName
	}

	if cr.CaCertDuration == time.Duration(0) {
		cr.CaCertDuration = defaultCaCertValidityDuration
	}

	if cr.ServerCertDuration == time.Duration(0) {
		cr.ServerCertDuration = defaultServerCertValidityDuration
	}

	if cr.LookaheadInterval == time.Duration(0) {
		cr.LookaheadInterval = defaultLookaheadInterval
	}

	if cr.RotationCheckFrequency == time.Duration(0) {
		cr.RotationCheckFrequency = defaultRotationCheckFrequency
	}

	if cr.ExtKeyUsages == nil {
		cr.ExtKeyUsages = &[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}

	reconciler := &ReconcileWH{
		cache:                       cache,
		writer:                      mgr.GetClient(), // TODO
		scheme:                      mgr.GetScheme(),
		ctx:                         context.Background(),
		secretKey:                   cr.SecretKey,
		wasCAInjected:               cr.wasCAInjected,
		webhooks:                    cr.Webhooks,
		needLeaderElection:          cr.RequireLeaderElection,
		refreshCertIfNeededDelegate: cr.refreshCertIfNeeded,
		fieldOwner:                  cr.FieldOwner,
		certsMounted:                cr.certsMounted,
		certsNotMounted:             cr.certsNotMounted,
		enableReadinessCheck:        cr.EnableReadinessCheck,
	}
	if err := addController(mgr, reconciler, cr.controllerName); err != nil {
		return err
	}
	return nil
}

// addNamespacedCache will add a new namespace-scoped cache.Cache to the provided manager.
// Informers in the new cache will be scoped to the provided namespace for namespaced resources,
// but will still have cluster-wide visibility into cluster-scoped resources.
// The cache will be started by the manager when it starts, and consumers should synchronize on
// it using WaitForCacheSync().
func addNamespacedCache(mgr manager.Manager, cr *CertRotator, namespace string) (cache.Cache, error) {
	var namespaces map[string]cache.Config
	if namespace != "" {
		namespaces = map[string]cache.Config{
			namespace: {},
		}
	}

	c, err := cache.New(mgr.GetConfig(),
		cache.Options{
			Scheme:            mgr.GetScheme(),
			Mapper:            mgr.GetRESTMapper(),
			DefaultNamespaces: namespaces,
		})
	if err != nil {
		return nil, err
	}
	// Wrapping the cache to make sure it's also started when the manager
	// hasn't been leader elected and CertRotator.RequireLeaderElection is false.
	if err := mgr.Add(&cacheWrapper{Cache: c, needLeaderElection: cr.RequireLeaderElection}); err != nil {
		return nil, fmt.Errorf("registering namespaced cache: %w", err)
	}
	return c, nil
}

// SyncingReader is a reader that needs syncing prior to being usable.
type SyncingReader interface {
	client.Reader
	WaitForCacheSync(ctx context.Context) bool
}

// CertRotator contains cert artifacts and a channel to close when the certs are ready.
type CertRotator struct {
	reader SyncingReader
	writer client.Writer

	SecretKey      types.NamespacedName
	CertDir        string
	CAName         string
	CAOrganization string
	DNSName        string
	ExtraDNSNames  []string
	IsReady        chan struct{}
	Webhooks       []WebhookInfo
	// FieldOwner is the optional fieldmanager of the webhook updated fields.
	FieldOwner             string
	RestartOnSecretRefresh bool
	ExtKeyUsages           *[]x509.ExtKeyUsage
	// RequireLeaderElection should be set to true if the CertRotator needs to
	// be run in the leader election mode.
	RequireLeaderElection bool
	// CaCertDuration sets how long a CA cert will be valid for.
	CaCertDuration time.Duration
	// ServerCertDuration sets how long a server cert will be valid for.
	ServerCertDuration time.Duration
	// RotationCheckFrequency sets how often the rotation is executed
	RotationCheckFrequency time.Duration
	// LookaheadInterval sets how long before the certificate is renewed
	LookaheadInterval time.Duration
	// CertName and Keyname override certificate path
	CertName string
	KeyName  string

	// EnableReadinessCheck if true, reconcilation loop will wait for controller-runtime's
	// runnable to finish execution.
	EnableReadinessCheck bool

	certsMounted    chan struct{}
	certsNotMounted chan struct{}
	wasCAInjected   *atomic.Bool
	caNotInjected   chan struct{}

	// testNoBackgroundRotation doesn't actually start the rotator in the background.
	// This should only be used for testing.
	testNoBackgroundRotation bool
	// controllerName allows setting the controller name to register it multiple times
	// fixing the new k8s check that produces 'controller with name cert-rotator already exists'
	// This should only be used for testing.
	controllerName string
}

func (cr *CertRotator) NeedLeaderElection() bool {
	return cr.RequireLeaderElection
}

// Start starts the CertRotator runnable to rotate certs and ensure the certs are ready.
func (cr *CertRotator) Start(ctx context.Context) error {
	if cr.reader == nil {
		return errors.New("nil reader")
	}
	if !cr.reader.WaitForCacheSync(ctx) {
		return errors.New("failed waiting for reader to sync")
	}

	// explicitly rotate on the first round so that the certificate
	// can be bootstrapped, otherwise manager exits before a cert can be written
	crLog.Info("starting cert rotator controller")
	defer crLog.Info("stopping cert rotator controller")
	if _, err := cr.refreshCertIfNeeded(); err != nil {
		crLog.Error(err, "could not refresh cert on startup")
		return err
	}

	// Once the certs are ready, close the channel.
	go cr.ensureCertsMounted()
	go cr.ensureReady()

	ticker := time.NewTicker(cr.RotationCheckFrequency)

tickerLoop:
	for {
		select {
		case <-ticker.C:
			if _, err := cr.refreshCertIfNeeded(); err != nil {
				crLog.Error(err, "error rotating certs")
			}
		case <-ctx.Done():
			break tickerLoop
		case <-cr.certsNotMounted:
			return errors.New("could not mount certs")
		case <-cr.caNotInjected:
			return errors.New("could not inject certs to webhooks")
		}
	}

	ticker.Stop()
	return nil
}

// refreshCertIfNeeded returns true if the CA was rotated
// and if there's any error when rotating the CA or refreshing the certs.
func (cr *CertRotator) refreshCertIfNeeded() (bool, error) {
	var rotatedCA bool

	refreshFn := func() (bool, error) {
		secret := &corev1.Secret{}
		if err := cr.reader.Get(context.Background(), cr.SecretKey, secret); err != nil {
			return false, errors.Wrap(err, "acquiring secret to update certificates")
		}
		if secret.Data == nil || !cr.validCACert(secret.Data[caCertName], secret.Data[caKeyName]) {
			crLog.Info("refreshing CA and server certs")
			if err := cr.refreshCerts(true, secret); err != nil {
				crLog.Error(err, "could not refresh CA and server certs")
				return false, nil
			}
			rotatedCA = true
			crLog.Info("server certs refreshed")
			if cr.RestartOnSecretRefresh {
				crLog.Info("Secrets have been updated; exiting so pod can be restarted (This behaviour can be changed with the option RestartOnSecretRefresh)")
				os.Exit(0)
			}
			return true, nil
		}
		// make sure our reconciler is initialized on startup (either this or the above refreshCerts() will call this)
		if !cr.validServerCert(secret.Data[caCertName], secret.Data[cr.CertName], secret.Data[cr.KeyName]) {
			crLog.Info("refreshing server certs")
			if err := cr.refreshCerts(false, secret); err != nil {
				crLog.Error(err, "could not refresh server certs")
				return false, nil
			}
			crLog.Info("server certs refreshed")
			if cr.RestartOnSecretRefresh {
				crLog.Info("Secrets have been updated; exiting so pod can be restarted (This behaviour can be changed with the option RestartOnSecretRefresh)")
				os.Exit(0)
			}
			return true, nil
		}
		crLog.Info("no cert refresh needed")
		return true, nil
	}
	if err := wait.ExponentialBackoff(wait.Backoff{
		Duration: 10 * time.Millisecond,
		Factor:   2,
		Jitter:   1,
		Steps:    10,
	}, refreshFn); err != nil {
		return rotatedCA, err
	}
	return rotatedCA, nil
}

func (cr *CertRotator) refreshCerts(refreshCA bool, secret *corev1.Secret) error {
	var caArtifacts *KeyPairArtifacts
	now := time.Now()
	begin := now.Add(-1 * time.Hour)
	if refreshCA {
		end := now.Add(cr.CaCertDuration)
		var err error
		caArtifacts, err = cr.CreateCACert(begin, end)
		if err != nil {
			return err
		}
	} else {
		var err error
		caArtifacts, err = buildArtifactsFromSecret(secret)
		if err != nil {
			return err
		}
	}
	end := now.Add(cr.ServerCertDuration)
	cert, key, err := cr.CreateCertPEM(caArtifacts, begin, end)
	if err != nil {
		return err
	}
	if err := cr.writeSecret(cert, key, caArtifacts, secret); err != nil {
		return err
	}
	return nil
}

func injectCert(updatedResource *unstructured.Unstructured, certPem []byte, webhookType WebhookType) error {
	switch webhookType {
	case Validating:
		return injectCertToWebhook(updatedResource, certPem)
	case Mutating:
		return injectCertToWebhook(updatedResource, certPem)
	case CRDConversion:
		return injectCertToConversionWebhook(updatedResource, certPem)
	case APIService:
		return injectCertToApiService(updatedResource, certPem)
	case ExternalDataProvider:
		return injectCertToExternalDataProvider(updatedResource, certPem)
	}
	return fmt.Errorf("incorrect webhook type")
}

func injectCertToWebhook(wh *unstructured.Unstructured, certPem []byte) error {
	webhooks, found, err := unstructured.NestedSlice(wh.Object, "webhooks")
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	for i, h := range webhooks {
		hook, ok := h.(map[string]interface{})
		if !ok {
			return errors.Errorf("webhook %d is not well-formed", i)
		}
		if err := unstructured.SetNestedField(hook, base64.StdEncoding.EncodeToString(certPem), "clientConfig", "caBundle"); err != nil {
			return err
		}
		webhooks[i] = hook
	}
	if err := unstructured.SetNestedSlice(wh.Object, webhooks, "webhooks"); err != nil {
		return err
	}
	return nil
}

func injectCertToConversionWebhook(crd *unstructured.Unstructured, certPem []byte) error {
	_, found, err := unstructured.NestedMap(crd.Object, "spec", "conversion", "webhook", "clientConfig")
	if err != nil {
		return err
	}
	if !found {
		return errors.New("`conversion.webhook.clientConfig` field not found in CustomResourceDefinition")
	}
	if err := unstructured.SetNestedField(crd.Object, base64.StdEncoding.EncodeToString(certPem), "spec", "conversion", "webhook", "clientConfig", "caBundle"); err != nil {
		return err
	}

	return nil
}

func injectCertToApiService(apiService *unstructured.Unstructured, certPem []byte) error {
	_, found, err := unstructured.NestedMap(apiService.Object, "spec")
	if err != nil {
		return err
	}
	if !found {
		return errors.New("`spec` field not found in APIService")
	}
	if err := unstructured.SetNestedField(apiService.Object, base64.StdEncoding.EncodeToString(certPem), "spec", "caBundle"); err != nil {
		return err
	}

	return nil
}

func injectCertToExternalDataProvider(externalDataProvider *unstructured.Unstructured, certPem []byte) error {
	_, found, err := unstructured.NestedMap(externalDataProvider.Object, "spec")
	if err != nil {
		return err
	}
	if !found {
		return errors.New("`spec` field not found in Provider")
	}
	if err := unstructured.SetNestedField(externalDataProvider.Object, base64.StdEncoding.EncodeToString(certPem), "spec", "caBundle"); err != nil {
		return err
	}

	return nil
}

func (cr *CertRotator) writeSecret(cert, key []byte, caArtifacts *KeyPairArtifacts, secret *corev1.Secret) error {
	populateSecret(cert, key, cr.CertName, cr.KeyName, caArtifacts, secret)
	return cr.writer.Update(context.Background(), secret)
}

// KeyPairArtifacts stores cert artifacts.
type KeyPairArtifacts struct {
	Cert    *x509.Certificate
	Key     *rsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

func populateSecret(cert, key []byte, certName string, keyName string, caArtifacts *KeyPairArtifacts, secret *corev1.Secret) {
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[caCertName] = caArtifacts.CertPEM
	secret.Data[caKeyName] = caArtifacts.KeyPEM
	secret.Data[certName] = cert
	secret.Data[keyName] = key
}

func buildArtifactsFromSecret(secret *corev1.Secret) (*KeyPairArtifacts, error) {
	caPem, ok := secret.Data[caCertName]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Cert secret is not well-formed, missing %s", caCertName))
	}
	keyPem, ok := secret.Data[caKeyName]
	if !ok {
		return nil, errors.New(fmt.Sprintf("Cert secret is not well-formed, missing %s", caKeyName))
	}
	caDer, _ := pem.Decode(caPem)
	if caDer == nil {
		return nil, errors.New("bad CA cert")
	}
	caCert, err := x509.ParseCertificate(caDer.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "while parsing CA cert")
	}
	keyDer, _ := pem.Decode(keyPem)
	if keyDer == nil {
		return nil, errors.New("bad CA cert")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyDer.Bytes)
	if err != nil {
		return nil, errors.Wrap(err, "while parsing CA key")
	}
	return &KeyPairArtifacts{
		Cert:    caCert,
		CertPEM: caPem,
		KeyPEM:  keyPem,
		Key:     key,
	}, nil
}

// CreateCACert creates the self-signed CA cert and private key that will
// be used to sign the server certificate.
func (cr *CertRotator) CreateCACert(begin, end time.Time) (*KeyPairArtifacts, error) {
	templ := &x509.Certificate{
		SerialNumber: big.NewInt(0),
		Subject: pkix.Name{
			CommonName:   cr.CAName,
			Organization: []string{cr.CAOrganization},
		},
		DNSNames: []string{
			cr.CAName,
		},
		NotBefore:             begin,
		NotAfter:              end,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, errors.Wrap(err, "generating key")
	}
	der, err := x509.CreateCertificate(rand.Reader, templ, templ, key.Public(), key)
	if err != nil {
		return nil, errors.Wrap(err, "creating certificate")
	}
	certPEM, keyPEM, err := pemEncode(der, key)
	if err != nil {
		return nil, errors.Wrap(err, "encoding PEM")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, errors.Wrap(err, "parsing certificate")
	}

	return &KeyPairArtifacts{Cert: cert, Key: key, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// CreateCertPEM takes the results of CreateCACert and uses it to create the
// PEM-encoded public certificate and private key, respectively.
func (cr *CertRotator) CreateCertPEM(ca *KeyPairArtifacts, begin, end time.Time) ([]byte, []byte, error) {
	dnsNames := []string{cr.DNSName}
	dnsNames = append(dnsNames, cr.ExtraDNSNames...)
	templ := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: cr.DNSName,
		},
		DNSNames:              dnsNames,
		NotBefore:             begin,
		NotAfter:              end,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           *cr.ExtKeyUsages,
		BasicConstraintsValid: true,
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, errors.Wrap(err, "generating key")
	}
	der, err := x509.CreateCertificate(rand.Reader, templ, ca.Cert, key.Public(), ca.Key)
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating certificate")
	}
	certPEM, keyPEM, err := pemEncode(der, key)
	if err != nil {
		return nil, nil, errors.Wrap(err, "encoding PEM")
	}
	return certPEM, keyPEM, nil
}

// pemEncode takes a certificate and encodes it as PEM.
func pemEncode(certificateDER []byte, key *rsa.PrivateKey) ([]byte, []byte, error) {
	certBuf := &bytes.Buffer{}
	if err := pem.Encode(certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}); err != nil {
		return nil, nil, errors.Wrap(err, "encoding cert")
	}
	keyBuf := &bytes.Buffer{}
	if err := pem.Encode(keyBuf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}); err != nil {
		return nil, nil, errors.Wrap(err, "encoding key")
	}
	return certBuf.Bytes(), keyBuf.Bytes(), nil
}

func (cr *CertRotator) lookaheadTime() time.Time {
	return time.Now().Add(cr.LookaheadInterval)
}

func (cr *CertRotator) validServerCert(caCert, cert, key []byte) bool {
	valid, err := ValidCert(caCert, cert, key, cr.DNSName, cr.ExtKeyUsages, cr.lookaheadTime())
	if err != nil {
		return false
	}
	return valid
}

func (cr *CertRotator) validCACert(cert, key []byte) bool {
	valid, err := ValidCert(cert, cert, key, cr.CAName, nil, cr.lookaheadTime())
	if err != nil {
		return false
	}
	return valid
}

func ValidCert(caCert, cert, key []byte, dnsName string, keyUsages *[]x509.ExtKeyUsage, at time.Time) (bool, error) {
	if len(caCert) == 0 || len(cert) == 0 || len(key) == 0 {
		return false, errors.New("empty cert")
	}

	pool := x509.NewCertPool()
	caDer, _ := pem.Decode(caCert)
	if caDer == nil {
		return false, errors.New("bad CA cert")
	}
	cac, err := x509.ParseCertificate(caDer.Bytes)
	if err != nil {
		return false, errors.Wrap(err, "parsing CA cert")
	}
	pool.AddCert(cac)

	_, err = tls.X509KeyPair(cert, key)
	if err != nil {
		return false, errors.Wrap(err, "building key pair")
	}

	b, _ := pem.Decode(cert)
	if b == nil {
		return false, errors.New("bad private key")
	}

	crt, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return false, errors.Wrap(err, "parsing cert")
	}

	opt := x509.VerifyOptions{
		DNSName:     dnsName,
		Roots:       pool,
		CurrentTime: at,
	}
	if keyUsages != nil {
		opt.KeyUsages = *keyUsages
	}

	_, err = crt.Verify(opt)
	if err != nil {
		return false, errors.Wrap(err, "verifying cert")
	}
	return true, nil
}

func reconcileSecretAndWebhookMapFunc(webhook WebhookInfo, r *ReconcileWH) func(ctx context.Context, object *unstructured.Unstructured) []reconcile.Request {
	return func(ctx context.Context, object *unstructured.Unstructured) []reconcile.Request {
		whKey := types.NamespacedName{Name: webhook.Name}
		if object.GetNamespace() != whKey.Namespace {
			return nil
		}
		if object.GetName() != whKey.Name {
			return nil
		}
		return []reconcile.Request{{NamespacedName: r.secretKey}}
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler.
func addController(mgr manager.Manager, r *ReconcileWH, controllerName string) error {
	// Create a new controller
	c, err := controller.NewUnmanaged(controllerName, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	err = c.Watch(
		source.Kind(r.cache, &corev1.Secret{}, &handler.TypedEnqueueRequestForObject[*corev1.Secret]{}),
	)
	if err != nil {
		return fmt.Errorf("watching Secrets: %w", err)
	}

	for _, webhook := range r.webhooks {
		wh := &unstructured.Unstructured{}
		wh.SetGroupVersionKind(webhook.gvk())
		err = c.Watch(
			source.Kind(r.cache, wh, handler.TypedEnqueueRequestsFromMapFunc(reconcileSecretAndWebhookMapFunc(webhook, r))),
		)

		if err != nil {
			return fmt.Errorf("watching webhook %s: %w", webhook.Name, err)
		}
	}

	return mgr.Add(controllerWrapper{c, r.needLeaderElection})
}

var _ reconcile.Reconciler = &ReconcileWH{}

// ReconcileWH reconciles a validatingwebhookconfiguration, making sure it
// has the appropriate CA cert.
type ReconcileWH struct {
	writer                      client.Writer
	cache                       cache.Cache
	scheme                      *runtime.Scheme
	ctx                         context.Context
	secretKey                   types.NamespacedName
	webhooks                    []WebhookInfo
	wasCAInjected               *atomic.Bool
	needLeaderElection          bool
	refreshCertIfNeededDelegate func() (bool, error)
	fieldOwner                  string
	certsMounted                chan struct{}
	certsNotMounted             chan struct{}
	enableReadinessCheck        bool
}

// Reconcile reads that state of the cluster for a validatingwebhookconfiguration
// object and makes sure the most recent CA cert is included.
func (r *ReconcileWH) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if request.NamespacedName != r.secretKey {
		return reconcile.Result{}, nil
	}

	if r.enableReadinessCheck {
		select {
		case <-r.certsMounted:
			// Continue with reconciliation
		case <-ctx.Done():
			// controller-runtime will requeue with backoff strategy when this error is returned
			return reconcile.Result{}, fmt.Errorf("context done, retrying reconciliation: %w", ctx.Err())
		case <-r.certsNotMounted:
			// controller-runtime will requeue with backoff strategy when this error is returned
			return reconcile.Result{}, errors.New("certs not mounted, retrying reconciliation")
		}
	}

	if !r.cache.WaitForCacheSync(ctx) {
		return reconcile.Result{}, errors.New("cache not ready")
	}

	secret := &corev1.Secret{}
	if err := r.cache.Get(r.ctx, request.NamespacedName, secret); err != nil {
		if k8sErrors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			// For additional cleanup logic use finalizers.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{Requeue: true}, err
	}

	if secret.GetDeletionTimestamp().IsZero() {
		if r.refreshCertIfNeededDelegate != nil {
			rotatedCA, err := r.refreshCertIfNeededDelegate()
			if err != nil {
				crLog.Error(err, "error rotating certs on secret reconcile")
				return reconcile.Result{}, err
			}

			// if we did rotate the CA, the secret is stale so let's return
			if rotatedCA {
				return reconcile.Result{}, nil
			}
		}

		artifacts, err := buildArtifactsFromSecret(secret)
		if err != nil {
			crLog.Error(err, "secret is not well-formed, cannot update webhook configurations")
			return reconcile.Result{}, nil
		}

		// Ensure certs on webhooks
		if err := r.ensureCerts(artifacts.CertPEM); err != nil {
			return reconcile.Result{}, err
		}

		// Set CAInjected if the reconciler has not exited early.
		r.wasCAInjected.Store(true)
	}

	return reconcile.Result{}, nil
}

// ensureCerts returns an arbitrary error if multiple errors are encountered,
// while all the errors are logged.
// This is important to allow the controller to reconcile the secret. If an error
// is returned, request will be requeued, and the controller will attempt to reconcile
// the secret again.
// When an error is encountered for when processing a webhook, the error is logged, but
// following webhooks are also attempted to be updated. If multiple errors occur for different
// webhooks, only the last one will be returned. This is ok, as the returned error is only meant
// to indicate that reconciliation failed. The information about all the errors is passed not
// by the returned error, but rather in the logged errors.
func (r *ReconcileWH) ensureCerts(certPem []byte) error {
	var anyError error = nil

	for _, webhook := range r.webhooks {
		gvk := webhook.gvk()
		log := crLog.WithValues("name", webhook.Name, "gvk", gvk)
		updatedResource := &unstructured.Unstructured{}
		updatedResource.SetGroupVersionKind(gvk)
		if err := r.cache.Get(r.ctx, types.NamespacedName{Name: webhook.Name}, updatedResource); err != nil {
			if k8sErrors.IsNotFound(err) {
				log.Error(err, "Webhook not found. Unable to update certificate.")
				continue
			}
			anyError = err
			log.Error(err, "Error getting webhook for certificate update.")
			continue
		}
		if !updatedResource.GetDeletionTimestamp().IsZero() {
			log.Info("Webhook is being deleted. Unable to update certificate")
			continue
		}

		log.Info("Ensuring CA cert", "name", webhook.Name, "gvk", gvk)
		if err := injectCert(updatedResource, certPem, webhook.Type); err != nil {
			log.Error(err, "Unable to inject cert to webhook.")
			anyError = err
			continue
		}
		opts := []client.UpdateOption{}
		if r.fieldOwner != "" {
			opts = append(opts, client.FieldOwner(r.fieldOwner))
		}
		if err := r.writer.Update(r.ctx, updatedResource, opts...); err != nil {
			log.Error(err, "Error updating webhook with certificate")
			anyError = err
			continue
		}
	}
	return anyError
}

// ensureCertsMounted ensure the cert files exist.
func (cr *CertRotator) ensureCertsMounted() {
	checkFn := func() (bool, error) {
		certFile := cr.CertDir + "/" + cr.CertName
		_, err := os.Stat(certFile)
		if err == nil {
			return true, nil
		}
		return false, nil
	}
	if err := wait.ExponentialBackoff(wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2,
		Jitter:   1,
		Steps:    10,
	}, checkFn); err != nil {
		crLog.Error(err, "max retries for checking certs existence")
		close(cr.certsNotMounted)
		return
	}
	crLog.Info(fmt.Sprintf("certs are ready in %s", cr.CertDir))
	close(cr.certsMounted)
}

// ensureReady ensure the cert files exist and the CAs are injected.
func (cr *CertRotator) ensureReady() {
	<-cr.certsMounted
	checkFn := func() (bool, error) {
		return cr.wasCAInjected.Load(), nil
	}
	if err := wait.ExponentialBackoff(wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2,
		Jitter:   1,
		Steps:    10,
	}, checkFn); err != nil {
		crLog.Error(err, "max retries for checking CA injection")
		close(cr.caNotInjected)
		return
	}
	crLog.Info("CA certs are injected to webhooks")
	close(cr.IsReady)
}
