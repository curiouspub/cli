// Package pack turns an Astro project directory into the archive the CLI
// uploads.
//
// WHAT EXISTS TODAY IS THE WALK, THE LIMITS AND THE ARCHIVE. The walk
// produces one canonical, ordered file list — the forced exclusions
// applied first and absolutely, the project's own ignore rules after
// them with their full semantics — plus the links it skipped and what it
// has to say about the names it found. The limits read that list: how
// many files there are, how big the largest is, how big they are
// together, and, once the archive exists, how big it turned out. The
// archive turns the list into a deterministic gzipped tar: every header
// field normalised, the compression level pinned, files only, and a
// digest computed as the bytes are written. This package describes what
// it does rather than what it will do, because a package comment read by
// a stranger is a claim like any other.
//
// THREE OF THE FOUR LIMITS ARE ANSWERED BEFORE ANYTHING IS PACKED and
// the fourth cannot be, which is the whole of why the limits are not one
// function. Compressing a project to discover it was never going to be
// allowed spends the user's time on an answer the file list already
// held; but the archive adds bytes of its own, so a tree inside every
// source limit can still pack to something too large, and that refusal
// has nowhere to live but after the pack. Prepare is where the order
// between them is kept.
//
// ONE WALK, THREE READERS. The limits, the scan for hard-coded
// development URLs, and the archive all read the same list, so the list
// has to be canonical: sorted byte-wise on the slash-separated relative
// path, identical across two runs, across two machines, and independent
// of the order a filesystem hands its directories back in.
//
// IT IS NOT ONE OF THE PRE-FLIGHT CHECKS, and it is not run by their
// engine. The engine hands a check a filesystem seam that cannot express
// directory enumeration, and widening it would add a method for one
// caller that no check has any use for. What this package owes instead
// is the same SHAPE the engine returns — findings, and a manifest row
// for each question it claims — so the two producers meet in the leaf
// package that holds the result model, which is a leaf precisely so that
// this walk and the checks reading its output cannot import each other.
package pack
