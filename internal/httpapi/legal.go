package httpapi

import (
	"html/template"
	"log/slog"
	"net/http"
)

// Privacy and terms are server-rendered rather than React routes, for the same
// reason the status page is: they have to answer even when the frontend bundle
// is broken, and a search engine or a person reading them should not need to
// run JavaScript.
//
// This is a personal project with accounts and passwords on it, which is
// enough to owe people a plain statement of what is stored and what is not.

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	renderLegal(w, privacyHTML)
}

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	renderLegal(w, termsHTML)
}

func renderLegal(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := legalTmpl.Execute(w, template.HTML(body)); err != nil {
		slog.Error("render legal page", "err", err)
	}
}

var legalTmpl = template.Must(template.New("legal").Parse(`<!doctype html>
<title>BlunderNet</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
 body{font:17px/1.6 'IBM Plex Sans',system-ui,sans-serif;background:#1a1f27;color:#f1f4f8;
      max-width:40rem;margin:0 auto;padding:2.5rem 1.25rem 4rem}
 h1{font-size:1.5rem;margin:0 0 .25rem}
 h2{font-size:1.05rem;margin:1.75rem 0 .4rem}
 p,li{color:#b4c0cd;margin:0 0 .6rem}
 ul{padding-left:1.2rem}
 a{color:#4a90d9}
 .updated{font-size:.94rem;color:#8593a2;margin-bottom:2rem}
</style>
{{.}}
<p style="margin-top:2.5rem"><a href="/">Back to the site</a></p>
`))

const privacyHTML = `
<h1>Privacy</h1>
<p class="updated">Last updated 16 August 2026.</p>

<p>BlunderNet is a personal project I built and run myself. It is not a
company and there is nobody to sell your data to, which makes this short.</p>

<h2>What is stored</h2>
<ul>
  <li>Games you play: the moves, the result, and when it finished.</li>
  <li>A rating, calculated from those results.</li>
  <li>If you make an account, your username and a hash of your password. The
      password itself is never stored and cannot be recovered from the hash.</li>
  <li>A session cookie so you stay signed in. It holds a random value and
      nothing about you.</li>
  <li>Your IP address, briefly, in server logs and in the rate limiter that
      stops one person starting thousands of games.</li>
</ul>

<h2>What is not</h2>
<p>No analytics, no advertising, no third-party trackers, no email address. If
you play without signing up you are given an anonymous account with no
identifying information attached to it at all.</p>

<h2>Where it lives</h2>
<p>On one server I rent in Amazon's us-east-1 region. Nothing is shared with
anyone else.</p>

<h2>How long</h2>
<p>Anonymous accounts that never finished a game are deleted after 30 days.
Everything else stays until you ask me to remove it.</p>

<h2>Getting it removed</h2>
<p>Email me and I will delete your account and its games. There is no form for
this because I would be the one running the query either way.</p>
`

const termsHTML = `
<h1>Terms</h1>
<p class="updated">Last updated 16 August 2026.</p>

<p>BlunderNet is free to use and provided as is. It is a project I built to
learn, not a service with a support contract.</p>

<h2>What I promise</h2>
<p>Very little, honestly. It runs on a single server. It will sometimes be
down, and when it is down games in progress may be lost. The
<a href="/status">status page</a> shows what it is currently doing and the
targets it aims for.</p>

<h2>What I ask</h2>
<ul>
  <li>Do not attack it. Rate limits exist; hitting them is fine, working around
      them is not.</li>
  <li>One account per person is plenty.</li>
  <li>Do not use automated clients to farm rating.</li>
</ul>

<h2>Your account</h2>
<p>I can remove an account that is being used to abuse the site. You can have
yours deleted whenever you like, see <a href="/privacy">Privacy</a>.</p>

<h2>The engine</h2>
<p>The neural network is one I trained and it is not very strong. Nothing it
plays should be taken as chess instruction.</p>
`
