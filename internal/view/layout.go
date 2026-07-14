package view

// htmxConfig is the <meta name="htmx-config"> the layout carries.
//
// It exists for one rule: swap a 422. htmx's default is to swap 2xx and to
// treat every 4xx as an error it renders nothing for — which is right for a
// 404 and wrong for a refused form, because the refusal *is* the content. The
// body of a 422 here is the entry form again, with the values still in it and
// the reason under the field at fault; dropping it would leave the user
// looking at an unchanged form and no explanation.
//
// The alternative — answering 200 and putting the errors in the body — is what
// makes htmx swap it without being asked, and it is a lie: a 200 says the
// submission was accepted, and nothing was appended. The status line is the
// only part of the answer a cache, a log, or a test reads, so it is the part
// that has to be true.
//
// The rules are htmx's own defaults with one inserted: a 422 that swaps. Rules
// are matched in order and the first hit wins, so it has to be named before the
// [45].. that would otherwise catch it. Nothing else is changed — a 404 or a
// 500 still raises an error and swaps nothing, which is what those mean.
const htmxConfig = `{"responseHandling":[` +
	`{"code":"204","swap":false},` +
	`{"code":"[23]..","swap":true},` +
	`{"code":"422","swap":true},` +
	`{"code":"[45]..","swap":false,"error":true}` +
	`]}`
