package forge

import (
	"fmt"
	"strconv"
)

// maxNumberDigits bounds a PR number so it fits an int on every platform.
const maxNumberDigits = 9

// ParseNumber reads a PR number as a worker reports it ("42" or "#42"); anything else is ErrInvalidRequest, so a
// bad number fails before any forge call (see SCENARIOS GT-6).
func ParseNumber(s string) (int, error) {
	digits := s
	if len(digits) > 0 && digits[0] == '#' {
		digits = digits[1:]
	}
	valid := digits != "" && len(digits) <= maxNumberDigits
	for _, r := range digits {
		valid = valid && r >= '0' && r <= '9'
	}
	n, err := strconv.Atoi(digits)
	if !valid || err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: PR number %q; report it as digits, such as 42", ErrInvalidRequest, s)
	}
	return n, nil
}
