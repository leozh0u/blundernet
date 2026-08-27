package store

import (
	"context"
	"errors"
	"testing"
)

func testClassrooms(t *testing.T) (*Classrooms, *Users, context.Context) {
	t.Helper()
	archive, users, ctx := testArchive(t)
	return NewClassrooms(archive.Pool()), users, ctx
}

// account makes a real, non-guest user, since a classroom refuses guests.
func account(t *testing.T, users *Users, ctx context.Context, name string) *User {
	t.Helper()
	u, err := users.Create(ctx, name, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCreateMakesTheCallerACoach(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach1")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if room.Role != RoleCoach {
		t.Errorf("creator has role %q, want coach", room.Role)
	}
	if len(room.JoinCode) != joinCodeLen {
		t.Errorf("join code %q is %d characters, want %d", room.JoinCode, len(room.JoinCode), joinCodeLen)
	}
	if room.Members != 1 {
		t.Errorf("a new room holds %d members, want 1", room.Members)
	}
}

func TestJoinCodeSurvivesBeingTyped(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach2")
	student := account(t, users, ctx, "student2")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	// Lower case, with the grouping dash and a stray space, which is what
	// comes back when somebody reads it off a screen.
	typed := " " + room.JoinCode[:3] + "-" + room.JoinCode[3:] + " "
	joined, err := rooms.Join(ctx, student.ID, typed)
	if err != nil {
		t.Fatalf("joining with %q: %v", typed, err)
	}
	if joined.ID != room.ID {
		t.Errorf("joined %s, want %s", joined.ID, room.ID)
	}
	if joined.Role != RoleStudent {
		t.Errorf("joiner has role %q, want student", joined.Role)
	}
}

func TestGuestsCannotHoldAClassroom(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach3")
	guest, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := rooms.Create(ctx, guest.ID, "Ghost class"); !errors.Is(err, ErrGuestUser) {
		t.Errorf("a guest creating a room: %v, want ErrGuestUser", err)
	}
	if _, err := rooms.Join(ctx, guest.ID, room.JoinCode); !errors.Is(err, ErrGuestUser) {
		t.Errorf("a guest joining a room: %v, want ErrGuestUser", err)
	}
}

// The authorization test that matters. A stranger holding a classroom id is
// exactly the shape of the bug this site already had once, where holding a
// game id was enough to resign somebody else's rated game.
func TestAStrangerSeesNothing(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach4")
	stranger := account(t, users, ctx, "stranger4")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := rooms.Roster(ctx, room.ID, stranger.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("stranger reading the roster: %v, want ErrNotAMember", err)
	}
	if _, err := rooms.RotateCode(ctx, room.ID, stranger.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("stranger rotating the code: %v, want ErrNotAMember", err)
	}
	if err := rooms.Remove(ctx, room.ID, stranger.ID, coach.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("stranger removing the coach: %v, want ErrNotAMember", err)
	}
}

func TestAStudentSeesOnlyThemselves(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach5")
	alice := account(t, users, ctx, "alice5")
	bob := account(t, users, ctx, "bob5")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*User{alice, bob} {
		if _, err := rooms.Join(ctx, u.ID, room.JoinCode); err != nil {
			t.Fatal(err)
		}
	}

	seen, members, err := rooms.Roster(ctx, room.ID, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != alice.ID {
		t.Errorf("a student sees %d rows, want only their own", len(members))
	}
	// The room still reports its real size, because "you are one of three" is
	// not the same as handing over the other two.
	if seen.Members != 3 {
		t.Errorf("room reports %d members, want 3", seen.Members)
	}
	// The join code is a coach's to hand out.
	if seen.JoinCode != "" {
		t.Errorf("a student was given the join code %q", seen.JoinCode)
	}

	_, all, err := rooms.Roster(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("the coach sees %d rows, want 3", len(all))
	}
}

func TestRotatingTheCodeRetiresTheOldOne(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach6")
	student := account(t, users, ctx, "student6")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := rooms.RotateCode(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == room.JoinCode {
		t.Fatal("rotation returned the same code")
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); !errors.Is(err, ErrNoSuchCode) {
		t.Errorf("joining with the retired code: %v, want ErrNoSuchCode", err)
	}
	if _, err := rooms.Join(ctx, student.ID, fresh); err != nil {
		t.Errorf("joining with the new code: %v", err)
	}
}

func TestAClassroomKeepsACoach(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach7")
	student := account(t, users, ctx, "student7")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}

	// A student leaving is fine.
	if err := rooms.Remove(ctx, room.ID, student.ID, student.ID); err != nil {
		t.Fatal(err)
	}
	// The only coach leaving is not, and the refusal has to leave them in the
	// room rather than half removed.
	if err := rooms.Remove(ctx, room.ID, coach.ID, coach.ID); !errors.Is(err, ErrLastCoach) {
		t.Fatalf("the last coach leaving: %v, want ErrLastCoach", err)
	}
	if _, members, err := rooms.Roster(ctx, room.ID, coach.ID); err != nil {
		t.Fatal(err)
	} else if len(members) != 1 {
		t.Errorf("after the refused removal the room holds %d members, want 1", len(members))
	}
}

func TestJoiningTwiceDoesNotDemoteACoach(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "coach8")

	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, coach.ID, room.JoinCode); !errors.Is(err, ErrAlreadyIn) {
		t.Errorf("a coach joining their own room: %v, want ErrAlreadyIn", err)
	}
	role, err := rooms.roleOf(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleCoach {
		t.Errorf("the coach is now %q", role)
	}
}
