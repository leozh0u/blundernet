// What this site is, and how to use it.
//
// It exists because a stranger arriving from a link has no way to tell a real
// site from a scraped one, and the answer to that is a page that says who made
// it and what it does. It doubles as the help page, since the things worth
// explaining are the same things worth trusting.

export default function About() {
  return (
    <section className="about">
      <p className="about-lead">
        BlunderNet is a free chess trainer. Six million puzzles you can filter,
        a bot to play, and a review of any game you paste in. No adverts, no
        paid tier, no email address collected.
      </p>

      <h2>Why it is free</h2>
      <p>
        Analysis and coaching sit behind paywalls on every major chess site. I
        think chess is one of the better things a kid can spend time on, and
        most of how I think about problems came from playing it, so the parts
        that teach you should not be the parts you have to pay for.
      </p>

      <h2>What each part does</h2>

      <h3>Puzzles</h3>
      <p>
        Filter by rating, how many moves the answer is, the phase of the game,
        the tactic, or the opening it came from, then drill. Hints go in three
        steps: the piece lights up, then an arrow shows where it goes, then it
        plays the move. Puzzles you miss go on a list you can come back
        to, and every puzzle links to the real game it was taken from.
      </p>
      <p>
        <strong>Ranked</strong> keeps the answer on the server and moves a
        rating. <strong>Streak</strong> gets harder every solve and ends on one
        miss.
      </p>

      <h3>Review a game</h3>
      <p>
        Paste a game from anywhere and every move is judged by how much it
        changed your chances of winning, with an accuracy score for each side
        and the move the engine would have played instead. Stockfish does the
        analysis, so the advice is right.
      </p>

      <h3>Classroom</h3>
      <p>
        A coach starts a session and reads out a six character code. Everyone
        who joins keeps their own account and their own progress. The coach
        gets a board with the rules switched off for setting positions up, can
        put a position in front of the class as a question and see everyone's
        answers gathered by move, and can set homework as a theme and a number.
      </p>

      <h3>Playing</h3>
      <p>
        The bot is a neural network I trained, not a strong engine playing
        badly on purpose. That matters more than it sounds: a strong engine
        told to play weakly makes blunders no human would ever make, while a
        weak network trained on human games plays like a weak human. It is
        around 1000 rated, which is why it is the opponent and Stockfish is the
        analyst.
      </p>

      <h2>Accessibility</h2>
      <p>
        Every position is also written out in words, so a screen reader can
        read the board. Arrow keys walk any move list. The blindfold button on
        a puzzle hides the pieces without taking them off the board, which is a
        memory exercise if you can see and no change at all if you cannot.
      </p>

      <h2>Your account</h2>
      <p>
        The site never asks for an email address, so there is no reset link to
        send and nothing to leak. Every account gets one recovery code at
        signup instead, shown once. Keep it somewhere. Using it signs out every
        other session, because that is what you want if somebody else has your
        password.
      </p>
      <p>
        You can use most of the site without an account at all. Progress then
        lives in your browser and disappears if you clear it, which is why
        ranked puzzles and classrooms ask you to sign up first.
      </p>

      <h2>Who made it</h2>
      <p>
        I am Leo, a computer science student at Rice. I built the site, trained
        the engine, and run it on one small server. The puzzles come from the{' '}
        <a href="https://database.lichess.org/#puzzles" target="_blank" rel="noreferrer">
          Lichess open puzzle database
        </a>
        , which is CC0, and the analysis uses{' '}
        <a href="https://stockfishchess.org" target="_blank" rel="noreferrer">
          Stockfish
        </a>
        . The code is on{' '}
        <a href="https://github.com/leozh0u/blundernet" target="_blank" rel="noreferrer">
          GitHub
        </a>
        .
      </p>
      <p>
        If something is broken, the link in the footer goes straight to me.
      </p>
    </section>
  )
}
