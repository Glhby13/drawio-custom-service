package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"

	"github.com/Glhby13/drawio-custom-service/internal/model"
)

type req struct {
	DocID string     `json:"docId"`
	Op    string     `json:"op"` // init | apply | canon
	Ops   []model.Op `json:"ops,omitempty"`
}

type resp struct {
	OK      bool       `json:"ok"`
	Code    string     `json:"code,omitempty"`
	Msg     string     `json:"msg,omitempty"`
	Index   int        `json:"index,omitempty"`
	Applied []model.Op `json:"applied,omitempty"`
	Seq     int64      `json:"seq,omitempty"`
	Hash    string     `json:"hash,omitempty"`
}

func docHash(d *model.Doc) string {
	sum := sha256.Sum256([]byte(d.Canon()))
	return hex.EncodeToString(sum[:])
}

func main() {
	var mu sync.Mutex
	docs := map[string]*model.Doc{}
	get := func(id string) *model.Doc {
		d, ok := docs[id]
		if !ok {
			d = model.NewDoc()
			p := &model.Page{
				ID:    "p1",
				Name:  model.Reg{V: "Page-1"},
				Order: model.Reg{V: "000000"},
				Cells: map[string]*model.Cell{
					"0": {ID: "0", Kind: "-", Props: map[string]model.Reg{}},
					"1": {ID: "1", Kind: "-", Parent: model.Reg{V: "0"}, Order: model.Reg{V: "000000"}, Props: map[string]model.Reg{}},
				},
			}
			d.Pages["p1"] = p
			docs[id] = d
		}
		return d
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			_ = enc.Encode(resp{OK: false, Code: "bad_json", Msg: err.Error()})
			continue
		}
		mu.Lock()
		d := get(r.DocID)
		switch r.Op {
		case "init", "canon":
			_ = enc.Encode(resp{OK: true, Seq: d.Seq, Hash: docHash(d)})
		case "apply":
			out, oe := d.ApplyBatch(r.Ops)
			if oe != nil {
				_ = enc.Encode(resp{OK: false, Code: oe.Code, Msg: oe.Msg, Index: oe.Index, Seq: d.Seq})
			} else {
				_ = enc.Encode(resp{OK: true, Applied: out, Seq: d.Seq, Hash: docHash(d)})
			}
		default:
			_ = enc.Encode(resp{OK: false, Code: "unknown"})
		}
		mu.Unlock()
	}
}
