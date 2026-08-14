package paths

// ReadBootID returns the OS boot identity. Tests may replace this.
var ReadBootID = readBootID

// CurrentBootID returns the current OS boot identity.
func CurrentBootID() string {
	return ReadBootID()
}
