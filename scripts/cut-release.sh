#!/usr/bin/env bash
#
# cut-release.sh — the step a person runs before a release happens.
#
# A release here is started by pushing a tag, and a tag push fires the
# whole outward change at once: six binaries, a checksum file, a
# signature, a public release, and a write into the package-manager tap.
# None of it can be taken back once somebody has downloaded it.
#
# THIS SCRIPT IS NOT THE BRAKE. The brake is server-side: the publishing
# job runs in a deployment environment with a required reviewer, and
# nobody reaches it by declining to use a wrapper. What this script is
# for is the step BEFORE that — showing an operator the state their
# change is about to land on, and handing them the exact commands rather
# than making them compose two from memory at the end of a long day.
#
# So it does three things, in this order, and then it stops:
#
#   1. refuses an argument that is not a version, and one that is not the
#      version this commit declares;
#   2. enumerates what already exists — tags, published releases, the
#      registry's tags, the tap's contents — and distinguishes "there is
#      none" from "I could not find out", refusing on the second;
#   3. prints the two commands verbatim and stops, unless it is given
#      --yes.
#
# Step 2 is the half that is easy to leave out and the half that matters.
# A tool that reports ABSENCE when it means "I could not look" is worse
# than one that says nothing: it is a confident answer, one line above a
# decision, and it will be believed.
#
# It deliberately does NOT check that the commit is on the default
# branch, though it reports the branch. That rule has one home — the
# release workflow refuses a tag the default branch does not carry — and
# a second copy here would be a rule anybody can skip by not running this
# script, sitting next to the one nobody can.

set -euo pipefail

readonly REPO="curiouspub/cli"
readonly TAP="curiouspub/homebrew-tap"
readonly PACKAGE="curiouspub"

# EVERY COMMITTED COPY OF THE VERSION, as file:how-many-copies-it-holds.
# The package manifest holds one; the lockfile repeats it twice, once at
# its top level and once in the entry for the package itself. A guard
# reads this line and requires it to name every tracked file that
# carries the declared version, with the same counts, so a copy added
# somewhere else reds the suite rather than going unread here.
readonly VERSION_COPIES=("npm/package.json:1" "npm/package-lock.json:2")

# Distinct statuses, because a caller that cannot tell "the run could not
# be made" from "the run found a problem" reads a broken environment as a
# finding.
readonly EXIT_REFUSED=1     # something already exists, or the tree is dirty
readonly EXIT_USAGE=2       # the arguments are not usable
readonly EXIT_UNDETERMINED=3 # a question could not be answered at all

# usage is written with the shell's own printf rather than a heredoc
# through an external program, and that is load bearing rather than
# stylistic: the hermetic row that proves this script changes nothing
# without consent runs it with a PATH holding only stand-ins for git, gh
# and npm. Anything else this file reaches for would exit 127 there, and
# a refusal that is really a missing program is not the refusal the row
# is asserting.
usage() {
	printf 'usage: scripts/cut-release.sh <version> [--yes]\n\n'
	printf '  <version>   the release version, as a v-prefixed semantic version:\n'
	printf '              v1.2.3, or v1.2.3-rc.1 for a pre-release.\n'
	printf '  --yes       actually create and push the tag. Without it this script\n'
	printf '              enumerates, prints the commands, and changes nothing.\n'
}

fail_usage() {
	printf 'cut-release: %s\n\n' "$1" >&2
	usage >&2
	exit "$EXIT_USAGE"
}

fail_refused() {
	printf 'cut-release: %s\n' "$1" >&2
	exit "$EXIT_REFUSED"
}

# check_version_copies refuses a version this commit does not declare,
# everywhere it declares one.
#
# A VERSION THAT LIVES IN MORE THAN ONE FILE IS CHECKED FOR AGREEMENT
# BEFORE THE IRREVERSIBLE ACT, NOT AFTER. The package publish compares the
# tag with the package's version and refuses a mismatch, correctly — but
# it runs after the release is public, the binaries are downloadable and
# the tap has been written, so what it can do by then is decline to
# finish. A release was cut here that way once: every other channel
# shipped, the package stayed on the version before, and the tag could
# not be moved because people had already fetched what it named. The
# same comparison run here costs nothing and happens while the tag is
# still a line on a screen.
#
# It reads the files with the shell alone, for the reason usage does:
# the hermetic rows give this script no programs but the three
# stand-ins. It matches with case patterns rather than the shell's
# expression operator, because the one expression in this file is the
# definition of a release version and another reader takes it from here
# by being the only one.
#
# FEWER COPIES THAN EXPECTED IS UNDETERMINED, NOT AGREEMENT. A reader
# that found nothing to compare has not found agreement, and a check that
# passes over a file it could not read is the confident absence step 2
# exists to refuse.
check_version_copies() {
	local want="$1" entry file expected found line trimmed indent value in_self
	local -a disagree=()
	for entry in "${VERSION_COPIES[@]}"; do
		file="${entry%:*}"
		expected="${entry##*:}"
		if [ ! -r "$file" ]; then
			undetermined "the version $file declares" \
				"$file could not be read from the directory this was run in."
		fi
		found=0
		in_self=0
		while IFS= read -r line || [ -n "$line" ]; do
			line="${line%$'\r'}"
			trimmed="${line#"${line%%[![:space:]]*}"}"
			indent=$((${#line} - ${#trimmed}))
			case "$trimmed" in
			'"": {'*)
				in_self=1
				continue
				;;
			'}'*)
				if [ "$indent" -le 4 ]; then
					in_self=0
				fi
				continue
				;;
			'"version": "'*) ;;
			*) continue ;;
			esac
			# Only the top level, and the lockfile's entry for the package
			# itself: every dependency the lockfile lists has a version of
			# its own, and none of them is this one.
			if [ "$indent" -ne 2 ] && { [ "$in_self" -ne 1 ] || [ "$indent" -ne 6 ]; }; then
				continue
			fi
			value="${trimmed#'"version": "'}"
			value="${value%%\"*}"
			found=$((found + 1))
			printf '  %s: %s\n' "$file" "$value"
			if [ "$value" != "$want" ]; then
				disagree+=("$file says $value")
			fi
		done <"$file"
		if [ "$found" -ne "$expected" ]; then
			undetermined "the version $file declares" \
				"found $found copies of the version in it where $expected were expected."
		fi
	done
	if [ "${#disagree[@]}" -ne 0 ]; then
		printf '\n'
		fail_refused "$(printf 'the version being cut is %s, and this commit declares otherwise:\n' "$want"
			printf '  %s\n' "${disagree[@]}"
			printf 'The package publish builds every download address from that version and\n'
			printf 'would refuse after everything else had shipped. Change every copy to %s\n' "$want"
			printf 'in a commit, merge it, and cut the release from that commit.')"
	fi
	printf 'every committed copy agrees: %s\n\n' "$want"
}

# undetermined is the whole reason step 2 exists. Every caller of it has
# just asked the world a question and got something other than an answer.
undetermined() {
	printf 'cut-release: could not determine %s.\n' "$1" >&2
	printf '  %s\n' "$2" >&2
	printf 'Refusing rather than reporting an absence that is really a failure to look.\n' >&2
	exit "$EXIT_UNDETERMINED"
}

main() {
	local version="" yes=0

	while [ "$#" -gt 0 ]; do
		case "$1" in
		--yes) yes=1 ;;
		-h | --help)
			usage
			exit 0
			;;
		-*) fail_usage "unknown option: $1" ;;
		*)
			if [ -n "$version" ]; then
				fail_usage "unexpected argument: $1"
			fi
			version="$1"
			;;
		esac
		shift
	done

	if [ -z "$version" ]; then
		fail_usage "no version given"
	fi

	# THE VALIDATION HAPPENS FIRST, before anything is asked of the world.
	# A mistyped version that reaches a registry lookup has already told
	# somebody about a release that is not happening; a mistyped version
	# that reaches a tag push cannot be recalled from the machines that
	# already fetched it.
	if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
		fail_usage "\"$version\" is not a version: expected vMAJOR.MINOR.PATCH, optionally followed by a pre-release suffix"
	fi

	local tool
	for tool in git gh npm; do
		command -v "$tool" >/dev/null 2>&1 ||
			undetermined "the state a release would land on" \
				"$tool is not installed, so this script cannot look at all."
	done

	printf '\n=== what exists right now ===\n\n'

	# --- the working tree -------------------------------------------
	local dirty
	dirty="$(git status --porcelain)"
	if [ -n "$dirty" ]; then
		printf 'working tree: NOT CLEAN\n%s\n\n' "$dirty"
		fail_refused "the working tree has changes; a tag would name a commit that does not contain them"
	fi
	printf 'working tree: clean\n\n'

	# --- the version this commit declares ---------------------------
	#
	# After the clean-tree check on purpose: with the tree clean, the
	# files read here are the files the tag would name.
	printf 'declared version:\n'
	check_version_copies "${version#v}"

	local branch
	branch="$(git rev-parse --abbrev-ref HEAD)"
	printf 'current branch: %s\n' "$branch"
	printf '  (the release workflow refuses a tag the default branch does not carry;\n'
	printf '   this line is a report, not that check)\n\n'

	# --- tags already in this repository ----------------------------
	local tags
	tags="$(git tag -l 'v*')"
	if [ -z "$tags" ]; then
		printf 'version tags: none yet\n\n'
	else
		printf 'version tags:\n'
		printf '%s\n\n' "$tags"
	fi

	local existing
	while IFS= read -r existing; do
		if [ "$existing" = "$version" ]; then
			fail_refused "$version is already a tag here; a released version is never re-cut"
		fi
	done <<<"$tags"

	# --- releases already published ---------------------------------
	local releases rc
	rc=0
	releases="$(gh release list --repo "$REPO" --limit 20 2>&1)" || rc=$?
	if [ "$rc" -ne 0 ]; then
		undetermined "which releases are published" "gh release list exited $rc: $releases"
	fi
	if [ -z "$releases" ]; then
		printf 'published releases: none\n\n'
	else
		printf 'published releases:\n%s\n\n' "$releases"
		case "$releases" in
		*"$version"*)
			fail_refused "$version already appears in the published releases"
			;;
		esac
	fi

	# --- the registry -----------------------------------------------
	#
	# An unpublished package answers with a specific shape, OBSERVED from
	# the real registry on 2026-09-07 rather than guessed: a non-zero
	# exit carrying the string E404. Anything else non-zero is a question
	# that was not answered, and keying on an error shape nobody has seen
	# is how a tool learns to report absence for every kind of failure.
	#
	# THE READ IS LIVE. The client answers from a local cache unless told
	# not to, and a cached answer printed here is a stale fact one screen
	# above the decision to push a tag. It has happened: during one
	# release the cache answered a missing package for one that existed,
	# and an old newest version for one that had moved.
	local tagsjson
	rc=0
	tagsjson="$(npm view --prefer-online "$PACKAGE" dist-tags --json 2>&1)" || rc=$?
	if [ "$rc" -ne 0 ]; then
		case "$tagsjson" in
		*E404*) printf 'registry tags: the package is not published yet\n\n' ;;
		*) undetermined "the registry tags" "the registry read exited $rc: $tagsjson" ;;
		esac
	else
		printf 'registry tags:\n%s\n\n' "$tagsjson"
	fi

	# --- the tap ----------------------------------------------------
	#
	# Same distinction, same reasoning. The observed shape of "this path
	# does not exist" is a non-zero exit whose message carries HTTP 404;
	# an expired credential and a missing permission produce something
	# else, and both would otherwise render as "the tap is empty" one
	# line above a decision to write into it.
	local tapfiles
	rc=0
	tapfiles="$(gh api "repos/$TAP/contents/Formula" 2>&1)" || rc=$?
	if [ "$rc" -ne 0 ]; then
		case "$tapfiles" in
		*"HTTP 404"*) printf 'tap contents: nothing published to the tap yet\n\n' ;;
		*) undetermined "what the tap already contains" "gh api exited $rc: $tapfiles" ;;
		esac
	else
		printf 'tap contents:\n%s\n\n' "$tapfiles"
	fi

	# --- what would happen ------------------------------------------
	printf '=== what a release would do ===\n\n'
	printf 'Pushing this tag starts a workflow that builds six binaries, writes a\n'
	printf 'checksum file, signs it, publishes a release, and writes into the tap.\n'
	printf 'The publishing job waits for a reviewer to approve it; nothing before\n'
	printf 'that approval leaves this machine.\n\n'

	printf '=== the exact commands ===\n\n'
	printf '  git tag -a %s -m %s\n' "$version" "$version"
	printf '  git push origin refs/tags/%s\n\n' "$version"

	# THE STEP AFTER, printed here because here is where its reader is.
	# A command written down where the operator already is gets typed; one
	# written down in a document does not.
	#
	# THIS COMMENT USED TO SAY NOTHING BEFORE A REAL RELEASE COULD EXERCISE
	# THE SIGNATURE, and that sentence cost a tag. It was true of the
	# IDENTITY and false of everything else: a keyless signature needs an
	# approved run, but the argument list does not, and it was the argument
	# list that went stale when the signing tool's pin moved to a major
	# where the old output flags are insufficient. The first execution of
	# that code path was therefore the first release, which failed in it.
	# The pull-request check now signs with a key pair generated in the
	# job, so the flags below and the ones that produce the bundle both run
	# continuously; only the identity waits for a release.
	printf '=== after the release finishes, check the signature ===\n\n'
	printf '  cosign verify-blob \\\n'
	printf '    --certificate-identity-regexp "^https://github.com/%s/[.]github/workflows/release[.]yml@refs/tags/%s$" \\\n' "$REPO" "$version"
	printf '    --certificate-oidc-issuer https://token.actions.githubusercontent.com \\\n'
	printf '    --bundle checksums.txt.bundle \\\n'
	printf '    checksums.txt\n\n'
	printf 'A failure there means the artefacts cannot prove who built them, which is\n'
	printf 'the whole reason they are signed. Treat it as a release to withdraw.\n\n'

	# NOTARISATION CAN OUTLAST THE RELEASE TOOL'S WAIT, and the answer is a
	# re-run rather than a new version. The tool waits at most twenty
	# minutes, because its token lives as long as the wait and the notary
	# service refuses a token that lives longer. A new team's first
	# submission has been held for over an hour. A timeout fails the build
	# before anything is archived, so nothing has been published, and the
	# same run can simply be picked up again once the service has answered.
	printf '=== if notarisation times out ===\n\n'
	printf 'The build stops with "timeout waiting for notarize submission response"\n'
	printf 'before anything is archived or published. Nothing is lost and nothing is\n'
	printf 're-cut:\n\n'
	printf '  1. wait until the submission shows Accepted:\n'
	printf '       xcrun notarytool history --key <AuthKey_ID.p8> --key-id <key id> --issuer <issuer id>\n'
	printf '  2. re-run the failed job of the same run:\n'
	printf '       gh run list --repo %s --workflow Release --limit 1\n' "$REPO"
	printf '       gh run rerun <run id> --failed --repo %s\n\n' "$REPO"
	printf 'Each binary is submitted in turn, so a later one can time out as well.\n'
	printf 'The answer is the same.\n\n'

	# A FAILED SIGNER INSTALL IS THE SAME SHAPE. The signing tool is
	# installed and verified at the start of the publishing job, and that
	# verification reads a trust root from the signing service's own
	# repository. When that repository cannot be reached, the install
	# fails before anything is built, so nothing has been published and
	# the same run can be picked up once the service answers again.
	printf '=== if the signing tool fails to install ===\n\n'
	printf 'The job stops at the cosign install with "trusted root is required" or\n'
	printf '"Could not fetch trusted_root.json" (Sigstore TUF unreachable), before\n'
	printf 'anything is built or published. Nothing is lost and nothing is re-cut:\n'
	printf 're-run the failed job once the service answers again:\n\n'
	printf '       gh run rerun <run id> --failed --repo %s\n\n' "$REPO"

	if [ "$yes" -ne 1 ]; then
		printf 'Nothing has been changed. Re-run with --yes to execute exactly the two\n'
		printf 'commands above, or run them yourself.\n'
		exit 0
	fi

	printf 'Running them.\n\n'
	git tag -a "$version" -m "$version"
	git push origin "refs/tags/$version"
	printf '\nPushed. The publishing job is now waiting for a reviewer.\n'
}

main "$@"
