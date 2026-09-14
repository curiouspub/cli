package ui

// Stage is what a person was doing when a failure met them, as a value
// every construction site declares.
//
// # Why it is declared rather than derived
//
// A failure id names a cause, and one cause can meet a person at several
// points of a run: the closed door shuts on a deploy, a login, a build and
// an address, and each of those sites says so in its own sentence. The
// catalog used to record where a failure was raised by the package
// directory of the code raising it, which is where the code lives rather
// than where the person was — five of one id's six sentences sat inside
// one "stage" that way. So the stage is something each site SAYS, the way
// it says its id, and the compiler is what makes every site say it.
//
// # What a stage is, and what it is not
//
// A stage names a step of a run AS THE PERSON MEETS IT, in the words the
// failure's own sentence uses: deploys, logins, building. It is never a
// package, a file or a function name. Adding one is a product decision
// recorded beside its constant, because the catalog publishes these
// values and the troubleshooting section lists sentences under them.
type Stage string

// The declared stages. The first six are the ones the service-unavailable
// sentences name; the rest are what the remaining sites needed.
const (
	StageDeploys     Stage = "deploys"
	StageLogins      Stage = "logins"
	StageBuilding    Stage = "building"
	StageAddresses   Stage = "addresses"
	StageThisRequest Stage = "this request"
	StageBuildLog    Stage = "build log"

	// Sending the packed project to the upload address.
	StageUploads Stage = "uploads"
	// This machine, before anything is sent: the project directory, where
	// the login is kept, the API address and the temporary directory.
	StageThisMachine Stage = "this machine"
	// The pre-flight checks, which refuse before anything leaves the
	// machine.
	StagePreflight Stage = "pre-flight"
	// The waitlist, when the day's capacity is used up.
	StageWaitlist Stage = "waitlist"
	// A question curious needed to ask and could not.
	StageQuestions Stage = "questions"
	// A fault in curious itself, which can meet a person anywhere in a run.
	StageThisRun Stage = "this run"
)

// Stages is the declared set, as the catalog's generator and its rows
// read it.
var Stages = []Stage{
	StageDeploys, StageLogins, StageBuilding, StageAddresses, StageThisRequest,
	StageBuildLog, StageUploads, StageThisMachine, StagePreflight,
	StageWaitlist, StageQuestions, StageThisRun,
}
