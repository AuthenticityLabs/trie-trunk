package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

func certificatePool(name string) (*x509.CertPool, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("invalid CA certificate: %s", name)
	}
	return pool, nil
}

func serverTLS(cert, key, ca string) (*tls.Config, error) {
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	config := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	if ca != "" {
		config.ClientCAs, err = certificatePool(ca)
		if err != nil {
			return nil, err
		}
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

func clientTLS(ca, cert, key string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS13}
	var err error
	if ca != "" {
		config.RootCAs, err = certificatePool(ca)
		if err != nil {
			return nil, err
		}
	}
	if (cert == "") != (key == "") {
		return nil, fmt.Errorf("mTLS requires both --client-cert and --client-key")
	}
	if cert != "" {
		pair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{pair}
	}
	return config, nil
}
