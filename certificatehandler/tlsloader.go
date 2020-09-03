package certificatehandler

import (
	"crypto/tls"
	"github.com/fsnotify/fsnotify"
	"github.com/patrick246/imap/observability/logging"
	"time"
)

type Action string

const (
	getCertificate    Action = "get_certificate"
	reloadCertificate Action = "reload_certificate"
)

var log = logging.CreateLogger("tls-loader")

type TlsLoader struct {
	certfile    string
	keyfile     string
	cert        *tls.Certificate
	watcher     *fsnotify.Watcher
	ocspStapler *ocspStapler

	actions     chan Action
	Certificate chan *tls.Certificate
}

func New(certFile, keyFile string) (*TlsLoader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	loader := &TlsLoader{
		certFile,
		keyFile,
		nil,
		watcher,
		newStapler(5 * time.Second),
		make(chan Action),
		make(chan *tls.Certificate),
	}

	go loader.dispatcher()
	loader.actions <- reloadCertificate

	err = watcher.Add(certFile)
	if err != nil {
		return nil, err
	}
	go loader.watcherLoop()

	return loader, nil
}

func (loader *TlsLoader) dispatcher() {
	for {
		action := <-loader.actions
		switch action {
		case getCertificate:
			log.Debugw("getting certificate")
			cert, err := loader.ocspStapler.stapleOcspResponse(*loader.cert)
			if err != nil {
				log.Warnw("ocsp stapling error", "error", err)
			}
			loader.Certificate <- &cert
		case reloadCertificate:
			log.Infow("reloading keypair", "certfile", loader.certfile, "keyfile", loader.keyfile)
			cer, err := tls.LoadX509KeyPair(loader.certfile, loader.keyfile)
			if err != nil {
				log.Errorw("certificate reload error", "error", err, "certfile", loader.certfile, "keyfile", loader.keyfile)
				continue
			}
			loader.cert = &cer
		}

	}
}

func (loader *TlsLoader) watcherLoop() {
	log.Infow("started certificate file watcher loop")
	for {
		select {
		case event, ok := <-loader.watcher.Events:
			if !ok {
				return
			}
			log.Debugw("fs watcher detected a change", "op", event.Op, "name", event.Name)
			if event.Op&fsnotify.Write == fsnotify.Write {
				loader.actions <- reloadCertificate
			}
		case err, ok := <-loader.watcher.Errors:
			if !ok {
				return
			}
			log.Errorw("certificate watch error", "error", err)
		}

	}
}

func (loader *TlsLoader) CertificateLoaderFunc() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(info *tls.ClientHelloInfo) (*tls.Certificate, error) {
		loader.actions <- getCertificate
		cer := <-loader.Certificate
		return cer, nil
	}
}
