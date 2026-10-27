package bootloader

import "strings"

// rpmvercmp is a port of rpm's rpmvercmp() (lib/rpmvercmp.c), the comparison
// rpm and dnf use to decide which of two package versions is newer. It
// returns -1, 0 or 1.
//
// The string is read as alternating runs of digits and letters; every other
// character only separates runs. Runs compare pairwise: two numeric runs by
// value (leading zeros ignored, so no overflow), two alphabetic runs bytewise,
// and a numeric run beats an alphabetic one. That last rule is the one a
// digits-only comparison gets wrong: in "553.el8_10" against "553.5.1.el8_10"
// the fifth run is "el" against "5", and the build with the extra numeric
// release component is the newer one.
//
// "~" sorts before everything, even the end of the string (1.0~rc1 < 1.0);
// "^" sorts after the end of the string but before any further run
// (1.0 < 1.0^git1 < 1.0.1). When every run compares equal, the side with
// characters left over is newer.
func rpmvercmp(a, b string) int {
	if a == b {
		return 0
	}
	one, two := a, b
	for len(one) > 0 || len(two) > 0 {
		one = strings.TrimLeftFunc(one, isRPMSeparator)
		two = strings.TrimLeftFunc(two, isRPMSeparator)

		// Tilde: sorts before everything else, including the end.
		if strings.HasPrefix(one, "~") || strings.HasPrefix(two, "~") {
			if !strings.HasPrefix(one, "~") {
				return 1
			}
			if !strings.HasPrefix(two, "~") {
				return -1
			}
			one, two = one[1:], two[1:]
			continue
		}

		// Caret: like tilde, except that the end of the string (the base
		// version) sorts before it.
		if strings.HasPrefix(one, "^") || strings.HasPrefix(two, "^") {
			if one == "" {
				return -1
			}
			if two == "" {
				return 1
			}
			if !strings.HasPrefix(one, "^") {
				return 1
			}
			if !strings.HasPrefix(two, "^") {
				return -1
			}
			one, two = one[1:], two[1:]
			continue
		}

		if one == "" || two == "" {
			break
		}

		var segOne, segTwo string
		isNum := isASCIIDigit(rune(one[0]))
		if isNum {
			segOne, one = splitRun(one, isASCIIDigit)
			segTwo, two = splitRun(two, isASCIIDigit)
		} else {
			segOne, one = splitRun(one, isASCIIAlpha)
			segTwo, two = splitRun(two, isASCIIAlpha)
		}

		// Different kinds of run: a numeric run is always newer than an
		// alphabetic one.
		if segTwo == "" {
			if isNum {
				return 1
			}
			return -1
		}

		if isNum {
			segOne = strings.TrimLeft(segOne, "0")
			segTwo = strings.TrimLeft(segTwo, "0")
			if len(segOne) != len(segTwo) {
				if len(segOne) > len(segTwo) {
					return 1
				}
				return -1
			}
		}
		if c := strings.Compare(segOne, segTwo); c != 0 {
			return c
		}
	}
	switch {
	case one == "" && two == "":
		return 0
	case one == "":
		return -1
	default:
		return 1
	}
}

// splitRun returns the leading run of s whose runes satisfy keep, and the rest.
func splitRun(s string, keep func(rune) bool) (run, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool { return !keep(r) })
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

func isASCIIAlpha(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// isRPMSeparator is rpm's "skip" class: anything that is not alphanumeric and
// not one of the two ordering markers.
func isRPMSeparator(r rune) bool {
	return !isASCIIDigit(r) && !isASCIIAlpha(r) && r != '~' && r != '^'
}
