// Package ratelimit identifies GitHub errors that carry a provider retry deadline.
package ratelimit

import "errors"

// Exceeded reports whether err contains an error identified as a GitHub rate limit.
func Exceeded(err error) bool {
	var limited interface{ RateLimited() bool }
	return errors.As(err, &limited) && limited.RateLimited()
}
