package domain

import (
	"net/netip"
	"strings"
)

// metadataHosts are the names cloud providers give their instance-metadata service.
var metadataHosts = map[string]bool{
	"metadata": true, "metadata.google.internal": true, "metadata.goog": true,
	"instance-data": true, "instance-data.ec2.internal": true,
}

// awsMetadataV6 is the AWS instance-metadata service's IPv6 address (IMDS on Nitro).
var awsMetadataV6 = netip.MustParseAddr("fd00:ec2::254")

// ForbiddenHost reports whether host (a name or an IP literal, with or without brackets)
// is link-local or a cloud metadata service: such addresses hand out the machine's own
// credentials, so cactusTrade never sends a request or a password there (PLAN 7.1.9). Names
// are checked as written; what a name resolves to is not (no I/O here).
func ForbiddenHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if metadataHosts[h] {
		return true
	}
	a, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a == awsMetadataV6
}

// IsLoopbackHost reports whether host is this machine: localhost or a loopback address.
func IsLoopbackHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.Unmap().IsLoopback()
}
