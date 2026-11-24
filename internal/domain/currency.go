package domain

import "strings"

// zeroDecimalCurrencies are the ISO 4217 currencies cactusTrade may meet whose minor unit is
// the unit itself (no decimals). Every other currency has two decimals.
var zeroDecimalCurrencies = map[string]bool{"JPY": true, "KRW": true}

// CurrencyDecimals is the number of decimals of an ISO 4217 currency's minor unit: 0 for JPY
// and KRW, 2 for every other currency. An amount of the currency in minor units (Cents) is
// amount × 10^CurrencyDecimals.
func CurrencyDecimals(code string) int {
	if zeroDecimalCurrencies[strings.ToUpper(strings.TrimSpace(code))] {
		return 0
	}
	return 2
}

// MinorUnitsPerUnit is 10^CurrencyDecimals(code): 1 for JPY, 100 for USD.
func MinorUnitsPerUnit(code string) int64 {
	if CurrencyDecimals(code) == 0 {
		return 1
	}
	return 100
}
