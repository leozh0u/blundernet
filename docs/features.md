# Features

## What is on the site

**Puzzles.** Filter by rating, solution length, game phase, theme and opening, then drill. Hints glow the piece, then draw an arrow, then play the move. Wrong answers go on a list you can drill again later, and there is a "another like this" link that is just the filter set as a URL.

**Saved, and what to work on.** A puzzle can be starred, and the saved list opened and cleared. Separately a panel names what you are worse at than the rest of your own play, and only that. Ranking every theme would look impressive and be mostly noise, as with forty puzzles behind you most of that ordering is luck, so the server decides what can honestly be said and the panel says nothing when there is nothing to say.

**Ranked puzzles.** The solution stays on the server. Both you and the puzzle carry a Glicko-2 rating, and a miss costs more the higher you climb: 1x at 1200, rising to 2.5x. Needs an account, which is the honest consequence of a rating meaning anything.

**Streak.** Puzzles climb 40 rating points per solve. One miss ends the run.

**Play the engine.** Six levels off one model. In learning games the bot adapts to you mid-game; in rated and friend games it never does.

**Play a friend** over a link, unrated.

**Review.** Every move is judged on how much it changed your chances of winning rather than on how many centipawns it cost, as +9 to +6 is three hundred centipawns and means nothing while +0.2 to -0.8 is a hundred and is the whole game. The conversion is Lichess's, published and derived from real games. Eight verdicts, brilliant down to blunder. Brilliant is a sacrifice that works: three pawns or more given up, counted after the opponent's best reply so an ordinary trade nets to nothing, with the engine agreeing anyway and the position not lost afterwards. Great is the only move that held, where everything else on the board drops at least twenty points of win percentage, which is the mistake threshold, so the claim is exact. Ten was tried first and handed out six of these in one game.

It reads a game pasted from anywhere, which is what makes it a tool rather than a feature of this site: the game a coach wants to go through with a student is almost never one played here.

**Classroom.** A coach starts a session, reads out a six character code, and whoever joins keeps their own account and their own progress. The coach gets a board with the rules switched off, the way a demo board works: any piece to any square legal or not, spare pieces in trays that never run out, drag one off the board to remove it. That position can go in front of the class as a question, and everyone answers by playing a move. The answers come back gathered by move with a count, because four people playing the same losing capture is a lesson and one person playing it is a typo. Homework is a theme, a rating window and a number; progress is counted from the attempts already recorded rather than a counter that can drift out of step.

Joining needs an account, which is the one place on the site that insists. Guest progress lives in a browser, so a roster of guests would empty itself the first time somebody cleared their cookies, and a coach could not tell that from a student who stopped turning up.

**The board can be read without being seen.** Every position is also written out in words for a screen reader, kings and queens before the pawns, and the result of a puzzle is announced rather than only coloured. A blindfold toggle hides the pieces while leaving them in place, so the board stays playable and stays readable: for a sighted player that is a memory exercise, and for a blind one the text was the board all along.

**Accounts recover without email.** The site collects no email address, so there is no reset link to send. Every account gets one recovery code at signup instead, shown once and stored as an Argon2id hash, in the same format and with the same parameters as a password. Spending a code retires it and signs out every other session, because recovery is what somebody reaches for when they think another person has their password.

**People can say what is broken.** A link in the footer takes a message and the page it came from, stored in Postgres rather than emailed. Open to signed-out visitors, because the person most likely to hit a bug is the one who just arrived and will not make an account to report it. No name field and no email field: asking for either is a reason not to bother. Deliberately keeps nothing else, no address and no browser fingerprint, because the privacy page says what is stored and quietly collecting more would make that untrue.

**The board has sound**, synthesised with the Web Audio API rather than sampled. Recorded clicks would mean a licence that permits commercial redistribution, hosting, and a few hundred kilobytes on a bundle that is 100KB gzipped. A move is a short pitched knock; a capture is lower and longer; check is two rising notes. It remembers being turned off.

