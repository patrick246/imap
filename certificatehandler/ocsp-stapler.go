package certificatehandler

import (
	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"golang.org/x/crypto/ocsp"
	"io/ioutil"
	"net/http"
	"strings"
	"time"
)

type ocspStapler struct {
	ocspCache map[string]ocspCacheEntry
	ttl       time.Duration
}

type ocspCacheEntry struct {
	response  []byte
	nextCheck time.Time
}

func newStapler(ttl time.Duration) *ocspStapler {
	return &ocspStapler{
		ocspCache: make(map[string]ocspCacheEntry),
		ttl:       ttl,
	}
}

/*
	Requests OCSP responses from the OCSP URL inside the certificate. If this URL is not present, the
	certificate is returned as-is. OCSP responses are cached for a configurable amount of nextCheck
*/
func (stapler *ocspStapler) stapleOcspResponse(cert tls.Certificate) (tls.Certificate, error) {
	x509Cert := cert.Leaf
	if x509Cert == nil {
		var err error
		x509Cert, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return cert, err
		}
		cert.Leaf = x509Cert
	}

	fingerprint := sha1.Sum(x509Cert.Raw)
	fingerprintStr := hex.EncodeToString(fingerprint[:])

	if len(x509Cert.OCSPServer) == 0 {
		log.Debugw("certificate without OCSP url, skipping OCSP stapling",
			"fingerprint", fingerprintStr,
			"cn", x509Cert.Subject.CommonName)
		return cert, nil
	}

	now := time.Now()
	if cachedEntry, ok := stapler.ocspCache[fingerprintStr]; ok && cachedEntry.nextCheck.Sub(now) > 10*time.Minute {
		cert.OCSPStaple = cachedEntry.response
		return cert, nil
	}

	urls := x509Cert.OCSPServer
	issuer, err := findLeafIssuer(cert)
	if err != nil {
		return cert, err
	}

	request, err := ocsp.CreateRequest(x509Cert, issuer, &ocsp.RequestOptions{})
	if err != nil {
		return cert, err
	}

	method := "POST"
	if len(request) < 189 {
		method = "GET"
	}

	response, err := tryOcspUrls(method, urls, request)
	if err != nil {
		return cert, err
	}

	parsedResponse, err := ocsp.ParseResponse(response, issuer)
	if err != nil {
		return cert, err
	}

	stapler.ocspCache[fingerprintStr] = ocspCacheEntry{
		response:  response,
		nextCheck: parsedResponse.NextUpdate,
	}

	cert.OCSPStaple = response
	return cert, nil
}

func findLeafIssuer(cert tls.Certificate) (*x509.Certificate, error) {
	leaf := cert.Leaf
	for _, chainCert := range cert.Certificate {
		chainParsed, err := x509.ParseCertificate(chainCert)
		if err != nil {
			log.Warnw("error parsing chain certificate", "error", err)
			continue
		}

		if bytes.Equal(leaf.RawIssuer, chainParsed.RawSubject) {
			return chainParsed, nil
		}
	}
	return nil, errors.New("could not find issuer in tls.Certificate")
}

func tryOcspUrls(method string, urls []string, request []byte) ([]byte, error) {
	for _, url := range urls {
		response, err := doOcspRequest(method, url, request)
		if err != nil {
			log.Warnw("ocsp request failed", "error", err)
		} else {
			return response, nil
		}
	}
	return nil, errors.New("ocsp url list exhausted")
}

func doOcspRequest(method, url string, request []byte) ([]byte, error) {
	var httpRequest *http.Request
	var httpErr error
	if method == "GET" {
		encodedPayload := base64.StdEncoding.EncodeToString(request)
		var payloadUrl string
		if strings.HasSuffix(url, "/") {
			payloadUrl = url + encodedPayload
		} else {
			payloadUrl = url + "/" + encodedPayload
		}
		httpRequest, httpErr = http.NewRequest("GET", payloadUrl, nil)
	} else {
		httpRequest, httpErr = http.NewRequest("POST", url, bytes.NewReader(request))
		if httpErr == nil {
			httpRequest.Header.Add(http.CanonicalHeaderKey("Content-Type"), "application/ocsp-request")
		}
	}

	if httpErr != nil {
		log.Warnw("ocsp request failed to create", "error", httpErr, "url", url)
		return nil, errors.New("failed to create request")
	}

	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		log.Warnw("ocsp request failed", "error", httpErr, "url", url)
		return nil, errors.New("failed to do request")
	}
	defer response.Body.Close()

	if (response.StatusCode / 100) != 2 {
		log.Warnw("ocsp responded with not 2xx code", "url", url, "code", response.StatusCode)
		log.Debugw("ocsp error body", "body", response.Body)
		return nil, errors.New("request failed with error status")
	}

	return ioutil.ReadAll(response.Body)
}
