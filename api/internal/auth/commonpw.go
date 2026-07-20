package auth

// The ~100 most common passwords (8+ chars only — shorter ones already fail the
// length rule). Registration rejects exact matches case-insensitively.
var commonPasswords = map[string]struct{}{}

func init() {
	for _, p := range []string{
		"password", "password1", "password123", "12345678", "123456789", "1234567890",
		"qwertyuiop", "qwerty123", "iloveyou", "sunshine", "princess", "football",
		"baseball", "welcome1", "abc12345", "monkey123", "letmein1", "shadow123",
		"superman", "michael1", "computer", "jennifer", "trustno1", "jordan23",
		"harley123", "hunter123", "ranger123", "buster123", "thomas123", "robert123",
		"soccer123", "batman123", "test1234", "pass1234", "killer123", "hockey123",
		"george123", "charlie1", "andrew123", "michelle", "jessica1", "pepper123",
		"daniel123", "access123", "123123123", "654321123", "maggie123", "starwars",
		"silver123", "william1", "dallas123", "yankees1", "123456789a", "987654321",
		"heather1", "hammer123", "summer123", "corvette", "taylor123", "austin123",
		"1234qwer", "q1w2e3r4", "qwer1234", "asdfghjkl", "zxcvbnm123", "asdf1234",
		"dragon123", "master123", "mustang1", "matrix123", "freedom1", "whatever1",
		"nicole123", "junior123", "anthony1", "friends1", "orange123", "camaro123",
		"secret123", "merlin123", "phoenix1", "mickey123", "bailey123", "knight123",
		"iceman123", "tigers123", "purple123", "united123", "aaaaaaaa", "11111111",
		"internet", "victoria", "melissa1", "marina123", "gateway1", "chelsea1",
		"diamond1", "scooter1", "richard1", "fuckyou1", "midnight", "blahblah",
		"spider123", "little123", "biteme123", "1qaz2wsx", "1q2w3e4r", "qazwsx123",
		"admin123", "administrator", "changeme", "letmein123", "welcome123",
	} {
		commonPasswords[p] = struct{}{}
	}
}

// IsCommonPassword reports whether pw is on the embedded deny list.
func IsCommonPassword(pw string) bool {
	_, hit := commonPasswords[toLowerASCII(pw)]
	return hit
}

func toLowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
