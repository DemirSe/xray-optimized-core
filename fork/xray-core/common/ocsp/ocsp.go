package ocsp

import (
	"bytes"
	"crypto/x509"
	"io"
	"net/http"

	"github.com/xtls/xray-core/common/errors"
	"golang.org/x/crypto/ocsp"
)

func GetOCSPForCert(cert [][]byte) ([]byte, error) {
	// ponytail: input is already DER; PEM round-trip added nothing.
	certificates := make([]*x509.Certificate, 0, len(cert))
	for _, derBytes := range cert {
		parsed, err := x509.ParseCertificate(derBytes)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, parsed)
	}
	if len(certificates) == 0 {
		return nil, errors.New("no certificates were found while parsing the bundle")
	}
	issuedCert := certificates[0]
	if len(issuedCert.OCSPServer) == 0 {
		return nil, errors.New("no OCSP server specified in cert")
	}
	if len(certificates) == 1 {
		if len(issuedCert.IssuingCertificateURL) == 0 {
			return nil, errors.New("no issuing certificate URL")
		}
		resp, errC := http.Get(issuedCert.IssuingCertificateURL[0])
		if errC != nil {
			return nil, errors.New("no issuing certificate URL")
		}
		defer resp.Body.Close()

		issuerBytes, errC := io.ReadAll(resp.Body)
		if errC != nil {
			return nil, errors.New(errC)
		}

		issuerCert, errC := x509.ParseCertificate(issuerBytes)
		if errC != nil {
			return nil, errors.New(errC)
		}

		certificates = append(certificates, issuerCert)
	}
	issuerCert := certificates[1]

	ocspReq, err := ocsp.CreateRequest(issuedCert, issuerCert, nil)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(ocspReq)
	req, err := http.Post(issuedCert.OCSPServer[0], "application/ocsp-request", reader)
	if err != nil {
		return nil, errors.New(err)
	}
	defer req.Body.Close()
	ocspResBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, errors.New(err)
	}
	return ocspResBytes, nil
}
