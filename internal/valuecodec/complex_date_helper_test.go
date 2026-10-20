package valuecodec_test

func complexDateMatches(value, want string) bool {
	return value == want || value == want+".000000"
}
