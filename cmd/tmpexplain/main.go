package main

import (
	"context"
	"fmt"
	"os"

	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

func main() {
	ctx := context.Background()
	a, err := store.NewArchive(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		panic(err)
	}
	p := store.NewPuzzles(a.Pool())
	rows, err := p.Select(ctx, store.Filter{}, 25, nil)
	if err != nil {
		panic(err)
	}
	bad := 0
	for _, q := range rows {
		e, ok := puzzle.Explain(q)
		if !ok {
			bad++
			fmt.Printf("FAILED %s %s %v\n", q.ID, q.FEN, q.Moves)
			continue
		}
		fmt.Printf("%-8s %4d %-11s %s | %v\n", q.ID, int(q.Rating), q.Phase, e.Headline, e.Points)
	}
	fmt.Println("unexplained:", bad, "of", len(rows))
}
