package lmtp

import "testing"

func TestNormalizeAddressPlusAddressing(t *testing.T) {
	testMail := "Test User <test+test@example.com>"
	expectedResult := "test@example.com"

	result, err := normalizeAddress(testMail)

	if err != nil {
		t.Fatalf("got error %v", err)
	}

	if result != expectedResult {
		t.Fatalf("expected %s, got %s", expectedResult, result)
	}
}

func TestNormalizeAddressUpercase(t *testing.T) {
	testMail := "Test User <TeSt@eXaMplE.cOm>"
	expectedResult := "test@example.com"

	result, err := normalizeAddress(testMail)

	if err != nil {
		t.Fatalf("got error %v", err)
	}

	if result != expectedResult {
		t.Fatalf("expected %s, got %s", expectedResult, result)
	}
}

func TestNormalizeAddressPunycode(t *testing.T) {
	testMail := "Test User <test@xn--4bi.example.com>"
	expectedResult := "test@xn--4bi.example.com"

	result, err := normalizeAddress(testMail)

	if err != nil {
		t.Fatalf("got error %v", err)
	}

	if result != expectedResult {
		t.Fatalf("expected %s, got %s", expectedResult, result)
	}
}

func TestNormalizeAddressUnicodeWithVariantSelector(t *testing.T) {
	testMail := "Test User <test@✉️.example.com>"
	expectedResult := "test@xn--4bi.example.com"

	result, err := normalizeAddress(testMail)

	if err != nil {
		t.Fatalf("got error %v", err)
	}

	if result != expectedResult {
		t.Fatalf("expected %s, got %s", expectedResult, result)
	}
}

func TestNormalizeAddressUnicode(t *testing.T) {
	testMail := "Test User <test@✉.example.com>"
	expectedResult := "test@xn--4bi.example.com"

	result, err := normalizeAddress(testMail)

	if err != nil {
		t.Fatalf("got error %v", err)
	}

	if result != expectedResult {
		t.Fatalf("expected %s, got %s", expectedResult, result)
	}
}
