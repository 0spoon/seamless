// The stale-assets scenario: the invisible-infrastructure beat. Users keep
// seeing the old stylesheet after a deploy, and the canonical quick fix -- a
// ?v= query string on the asset links -- silently does nothing here, because
// the CDN's cache key is path-only. Only the memory knows that; the repo
// cannot: the CDN's configuration is not in it. The fixture even baits the
// trap with a `version` const in main.go, one Sprintf away from ?v=1.4.2.
//
// Starting state: plan:asset-pipeline is 1/3 done with "make a deploy change
// the URL clients fetch" the one claimable step, a ~20h-old finding that
// locates the links (homeHTML in server.go) WITHOUT naming the constraint,
// and a memory set whose load-bearing item is cdn-query-string-blind. The
// right fix changes the asset PATHS -- content-hashed filenames or a
// versioned path segment; an arm without the memory reaches for the query
// string and ships a no-op.
package bench

import (
	"github.com/0spoon/seamless/internal/core"
	"github.com/0spoon/seamless/internal/demokit"
)

const (
	staleAssetsName = "stale-assets"
	// staleAssetsPlan is the seeded plan slug; its step tasks carry it.
	staleAssetsPlan = "asset-pipeline"
	// staleAssetsMemory is the scenario's load-bearing memory: its description
	// alone (the line the briefing's memory index shows) rules the query
	// string out and names the correct shape. The seed writes it and the
	// grader checks the agent consulted it, from this one name.
	staleAssetsMemory = "cdn-query-string-blind"
	// staleAssetsStep is the plan's sole claimable step -- the work the run is
	// graded on. Its wording restates the symptom and gives nothing away:
	// "change the URL" is true of the query-string trap too.
	staleAssetsStep = "Make a deploy change the URL clients fetch for each asset"
	// staleAssetsSession is the completed session the finding hangs off.
	staleAssetsSession = "cc/4c8f0d2e"
	// staleAssetsFinding is that session's ~20h-old summary. It confirms the
	// symptom and locates the links WITHOUT naming the constraint: the
	// query-string danger must reach the agent through the memory or not at
	// all, or the scenario measures nothing.
	staleAssetsFinding = "Confirmed the stale-styles reports: after a deploy the CDN keeps serving the old style.css until clients hard-refresh, because the asset URLs never change between releases. The asset links live in the homeHTML template in server.go, and the files ship from static/ via the file server. Next is step 2 -- make a deploy actually change what clients fetch."
)

// staleAssets is briefing-surfaced (finding + plan + memory index land at
// SessionStart), so it runs headless via plain `claude -p`.
var staleAssets = Scenario{
	Name:   staleAssetsName,
	Prompt: "After deploys, users keep getting the old stylesheet until they hard-refresh. Can you fix it so a deploy reliably picks up the new static assets?",
	Seed:   seedStaleAssets,
	Grader: staleAssetsGrader,
}

func seedStaleAssets(s *demokit.Seeder, repoPath string) error {
	if err := seedProject(s, repoPath); err != nil {
		return err
	}
	sess, err := seedFinishedSession(s, staleAssetsSession, "4c8f0d2e-77b3-49a1", staleAssetsFinding, 20, 35)
	if err != nil {
		return err
	}
	mems, err := noiseMemories(
		"refresh-token-single-use", "auth-cookies-httponly-secure",
		"auth-cookies-samesite-lax", "dashboard-latency-budget",
		"cdn-purge-by-tag", "deploy-runbook", "postgres-timeouts", "chroma-boot-race",
	)
	if err != nil {
		return err
	}
	// The load-bearing memory, on top of the common noise.
	mems = append(mems, seedMemory{core.KindGotcha, staleAssetsMemory,
		"The CDN's cache key is path-only: query strings are ignored, so ?v= cache-busting does nothing. Version the asset PATH instead.",
		"The CDN computes its cache key from the request path alone -- query strings are stripped before lookup. A ?v= or ?hash= suffix on an asset URL therefore busts nothing: every version of /static/style.css?v=N is the same cache entry, and clients keep the old bytes for the full TTL.\n\nWe shipped ?v= busting once. Every client kept the stale CSS until the TTL ran out, and support spent a morning telling people to hard-refresh.\n\nWhat works here: version the PATH -- content-hashed filenames (style.<hash>.css) or a release segment (/static/r42/...). A CDN purge is a stopgap for an incident, not a deploy mechanism (see cdn-purge-by-tag).", 5})
	if err := seedMemories(s, sess.Name, mems); err != nil {
		return err
	}
	if err := seedPlanSteps(s, staleAssetsPlan, sess.Name, []seedStep{
		{"Serve the dashboard's JS and CSS from the /static/ file server", true, -1, 4, 7},
		{staleAssetsStep, false, 0, 1, 0},
		{"Raise the asset max-age once the URLs are immutable", false, 1, 1, 0},
	}); err != nil {
		return err
	}
	// One failed trial: the purge-on-deploy escape, already tried and closed.
	// It reports what happened and prescribes nothing.
	return seedTrials(s, sess, []seedTrial{{
		lab:      "stale-assets",
		title:    "Purge the CDN after each deploy",
		changes:  "Added a full CDN purge call to the end of the deploy script.",
		expected: "Clients pick up the new assets within a minute of each deploy.",
		actual:   "Worked for the first deploy; the second deploy that day hit the provider's purge rate limit, and the rollback in between served stale CSS for 40 minutes. Purging is an incident tool, not a deploy mechanism.",
		outcome:  core.OutcomeFail,
		agoHours: 68,
	}})
}
