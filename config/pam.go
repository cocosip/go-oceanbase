package config

// PAMChallenge describes one UTF-8 dialog prompt. Round starts at one for each
// plugin use. Last is a server hint; authentication still requires a final OK.
type PAMChallenge struct {
	Round      int
	Prompt     string
	Echo, Last bool
}
