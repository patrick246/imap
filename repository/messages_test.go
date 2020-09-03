package repository

import (
	"github.com/emersion/go-imap"
	"testing"
)

func Test_mapSeqNumsToUids_Static(t *testing.T) {
	uids := []uint32{
		1, 2, 3, 8, 9, 10, 11, 12, 13, 18, 19, 20, 21, 22, 23,
	}

	var seqSetIn imap.SeqSet
	seqSetIn.AddRange(1, 4)
	seqSetIn.AddRange(8, 13)

	seqSetOut, err := mapSeqNumsToUids(&seqSetIn, uids)
	if err != nil {
		t.Fatal(err)
	}

	if len(seqSetOut.Set) != 4 {
		t.Fatalf("seq set length expected=4 got=%d, contents=%v", len(seqSetOut.Set), seqSetOut.Set)
	}

	// 1:3,8,12:13,18:21
	outRange := []uint32{
		1, 2, 3, 8, 12, 13, 18, 19, 20, 21,
	}
	for _, elem := range outRange {
		if !seqSetOut.Contains(elem) {
			t.Fatalf("seq set does not contain expected element %d", elem)
		}
	}

}

func Test_mapSeqNumsToUids_Dynamic(t *testing.T) {
	uids := []uint32{
		1, 2, 3, 8, 9, 10, 11, 12, 13, 18, 19, 20, 21, 22, 23,
	}

	var seqSetIn imap.SeqSet
	seqSetIn.AddRange(1, 4)
	seqSetIn.AddRange(8, 0)

	seqSetOut, err := mapSeqNumsToUids(&seqSetIn, uids)
	if err != nil {
		t.Fatal(err)
	}

	if len(seqSetOut.Set) != 4 {
		t.Fatalf("seq set length expected=4 got=%d, contents=%v", len(seqSetOut.Set), seqSetOut.Set)
	}

	// 1:3,8,12:13,18:21
	outRange := []uint32{
		1, 2, 3, 8, 12, 13, 18, 19, 20, 21, 22, 23,
	}
	for _, elem := range outRange {
		if !seqSetOut.Contains(elem) {
			t.Fatalf("seq set does not contain expected element %d", elem)
		}
	}
}

func Test_mapSeqNumsToUids_DynamicLargest(t *testing.T) {
	uids := []uint32{
		1, 2, 3, 8, 9, 10, 11, 12, 13, 18, 19, 20, 21, 22, 23,
	}

	var seqSetIn imap.SeqSet
	seqSetIn.AddRange(0, 0)

	seqSetOut, err := mapSeqNumsToUids(&seqSetIn, uids)
	if err != nil {
		t.Fatal(err)
	}

	if len(seqSetOut.Set) != 1 {
		t.Fatalf("seq set length expected=1 got=%d, contents=%v", len(seqSetOut.Set), seqSetOut.Set)
	}

	if seqSetOut.Set[0].Start != 23 {
		t.Fatalf("expected out seq start to be 23, got %d", seqSetOut.Set[0].Start)
	}

	if seqSetOut.Set[0].Stop != 23 {
		t.Fatalf("expected out seq stop to be 23, got %d", seqSetOut.Set[0].Stop)
	}
}

func Test_mapSeqNumsToUids_DynamicLargestMultipleRanges(t *testing.T) {
	uids := []uint32{
		1, 2, 3, 8, 9, 10, 11, 12, 13, 18, 19, 20, 21, 22, 23,
	}

	var seqSetIn imap.SeqSet
	seqSetIn.AddRange(1, 2)
	seqSetIn.AddRange(0, 0)

	seqSetOut, err := mapSeqNumsToUids(&seqSetIn, uids)
	if err != nil {
		t.Fatal(err)
	}

	if len(seqSetOut.Set) != 2 {
		t.Fatalf("seq set length expected=1 got=%d, contents=%v", len(seqSetOut.Set), seqSetOut.Set)
	}

	if seqSetOut.Set[0].Start != 1 {
		t.Fatalf("expected out seq start to be 1, got %d", seqSetOut.Set[0].Start)
	}

	if seqSetOut.Set[0].Stop != 2 {
		t.Fatalf("expected out seq stop to be 2, got %d", seqSetOut.Set[0].Stop)
	}

	if seqSetOut.Set[1].Start != 23 {
		t.Fatalf("expected out seq start to be 23, got %d", seqSetOut.Set[1].Start)
	}

	if seqSetOut.Set[1].Stop != 23 {
		t.Fatalf("expected out seq stop to be 23, got %d", seqSetOut.Set[2].Stop)
	}
}

func Test_mapSeqNumsToUids_NonExistentSeqNums(t *testing.T) {
	uids := []uint32{
		1, 2, 3,
	}

	var seqSetIn imap.SeqSet
	seqSetIn.AddRange(1, 5)

	seqSetOut, err := mapSeqNumsToUids(&seqSetIn, uids)
	if err == nil {
		t.Fatalf("expected err, got seqset %s", seqSetOut.String())
	}
}
