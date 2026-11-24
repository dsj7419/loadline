package doctor

// AncestorsToCheck and Symlinked expose to the external tests the folders
// the doctor looks for a link in, and its warning of one: a settings file
// the system cannot read, such as a reparse point no filter handles, never
// reaches that warning through Run.
var (
	AncestorsToCheck = ancestorsToCheck
	Symlinked        = symlinked
)
