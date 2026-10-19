package buildinfo

// Version is the agent/recovery pilot label. Release hashes are not this string.
// Release builds override this with -X; ordinary developer builds keep the default.
var Version = "0.1.1"
