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
//
// # One sentence each, published
//
// Every constant below carries exactly one sentence saying what a person
// is doing when a failure carries it. The catalog's note is generated from
// those sentences, so the key is readable by someone who never opens this
// file, and a row fails if a stage has none or the note leaves one out.
type Stage string

// The declared stages. The first six are the ones the service-unavailable
// sentences name; the rest are what the remaining sites needed.
const (
	// A person is starting a deploy: curious is asking whether there is room today and for somewhere to upload the project.
	StageDeploys Stage = "deploys"
	// A person is logging in, or curious is saving the login they just completed.
	StageLogins Stage = "logins"
	// A person is waiting while the server builds the project and decides whether to take the result.
	StageBuilding Stage = "building"
	// A person is waiting for a finished deploy to be given its public address.
	StageAddresses Stage = "addresses"
	// A person asked curious.pub for something and the server closed the door without saying which step it was.
	StageThisRequest Stage = "this request"
	// A person is watching the build log stream in while the build runs.
	StageBuildLog Stage = "build log"
	// A person is waiting while curious sends the packed project to the upload address.
	StageUploads Stage = "uploads"
	// A person is getting this machine ready before anything is sent: the project directory, where the login is kept, the API address or the temporary directory.
	StageThisMachine Stage = "this machine"
	// A person is having the project checked before anything leaves the machine.
	StagePreflight Stage = "pre-flight"
	// A person found today's capacity used up and is being offered the waitlist.
	StageWaitlist Stage = "waitlist"
	// A person is being asked a question that curious cannot put to them or cannot read the answer to.
	StageQuestions Stage = "questions"
	// A person could be anywhere in a run, and the fault is in curious itself.
	StageThisRun Stage = "this run"
)

// Stages is the declared set, in declared order, as the catalog's
// generator and its rows read it.
var Stages = []Stage{
	StageDeploys, StageLogins, StageBuilding, StageAddresses, StageThisRequest,
	StageBuildLog, StageUploads, StageThisMachine, StagePreflight,
	StageWaitlist, StageQuestions, StageThisRun,
}
