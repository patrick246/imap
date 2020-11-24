package lmtp

import (
	"errors"
	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
	"net/mail"
	"strings"
	"unicode"
)

func normalizeAddress(address string) (string, error) {
	parsedAddress, err := mail.ParseAddress(address)
	if err != nil {
		return "", err
	}

	lastAt := strings.LastIndex(parsedAddress.Address, "@")
	if lastAt < 0 {
		return "", errors.New("address missing domain part")
	}

	localPart, domainPart := parsedAddress.Address[:lastAt], parsedAddress.Address[lastAt+1:]

	domainPart, err = normalizeDomain(domainPart)
	if err != nil {
		return "", err
	}

	localPart, err = normalizeLocalPart(localPart)
	if err != nil {
		return "", err
	}

	return localPart + "@" + domainPart, nil
}

func normalizeDomain(domain string) (string, error) {
	domain = norm.NFC.String(domain)
	domain = strings.Map(func(r rune) rune {
		// Remove variant selectors for font rendering
		if unicode.IsPrint(r) && r&0xFFF0 != 0xFE00 {
			return r
		}
		return -1
	}, domain)
	domain = strings.ToLower(domain)
	return idna.ToASCII(domain)
}

func normalizeLocalPart(localPart string) (string, error) {
	localPart = strings.ToLower(localPart)

	plusIndex := strings.Index(localPart, "+")
	if plusIndex >= 0 {
		localPart = localPart[:plusIndex]
	}

	return localPart, nil
}
