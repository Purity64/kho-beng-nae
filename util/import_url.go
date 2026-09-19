package util

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"

	"Kho-beng-nae/lib/protocal"
)



type URL struct {
	Scheme  string
	Domain  string
	Port    uint16
	BaseURL string
	IsSubdomain	bool
	Protocol protocal.Protocol
}

type URLData struct {
	URLs []URL
}

func NewURLData() *URLData {
	return &URLData{
		URLs: make([]URL, 0, 4),
	}
}

func (u *URLData) Add(rawURLs ...string) error {
	for _, rawURL := range rawURLs {
		rawURL = strings.TrimSpace(rawURL)

		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", rawURL, err)
		}

		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf(
				"url %q must use http or https",
				rawURL,
			)
		}

		host := parsed.Hostname()

		if host == "" {
			return fmt.Errorf("url %q has no host", rawURL)
		}


		var port uint16

		if portString := parsed.Port(); portString != "" {
			p, err := strconv.ParseUint(portString, 10, 16)
			if err != nil {
				return fmt.Errorf(
					"invalid port in %q: %w",
					rawURL,
					err,
				)
			}

			port = uint16(p)
		} else {
			switch parsed.Scheme {
			case "https":
				port = 443
			case "http":
				port = 80
			}
		}

		baseURL := parsed.Scheme + "://" + parsed.Host

		baseURL = strings.TrimRight(baseURL, "/")

		protocol, err := protocal.CheckProtocol(baseURL)
		if err != nil {
			return fmt.Errorf(
				"failed to detect protocol for %q: %w",
				baseURL,
				err,
			)
		}

		u.URLs = append(u.URLs, URL{
			Scheme:  parsed.Scheme,
			Domain:  host,
			Port:    port,
			BaseURL: baseURL,
			IsSubdomain: CheckSubdomain(host),
			Protocol:  protocol,
		})
	}

	return nil
}

func CheckSubdomain(host string) bool {
	mainDomain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return false
	}

	cleanHost := strings.TrimPrefix(host, "www.")

	isSubdomain := cleanHost != mainDomain

	return isSubdomain
}