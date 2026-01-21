package sanitize

// DNS1123 converts s to a DNS-1123 subdomain-compatible string for resource names.
// It lowercases, replaces unsupported characters with '-', and trims leading/trailing non-alphanumerics.
func DNS1123(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			b = append(b, r+('a'-'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.':
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	// trim leading/trailing non-alphanumeric
	start, end := 0, len(b)
	for start < end {
		r := b[start]
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			break
		}
		start++
	}
	for end > start {
		r := b[end-1]
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			break
		}
		end--
	}
	if start >= end {
		return "unknown"
	}
	return string(b[start:end])
}
