// credit 工具：查询全部账号额度（日报）+ 手动申请额度。
// 用法: credit [-auth-dir ./auths] [-pretty] [-uid <uid>] [-json] [-apply register|campaign]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"catpaw2api/internal/auth"
	"catpaw2api/internal/upstream"
)

func main() {
	authDir := flag.String("auth-dir", "./auths", "auth dir")
	pretty := flag.Bool("pretty", false, "human readable report")
	jsonOut := flag.Bool("json", false, "raw JSON output")
	uid := flag.String("uid", "", "only this account")
	apply := flag.String("apply", "", "manual quota apply: register | campaign")
	flag.Parse()

	auths, err := auth.LoadDir(*authDir)
	if err != nil {
		log.Fatalf("load auths: %v", err)
	}
	if len(auths) == 0 {
		log.Fatalf("no accounts in %s", *authDir)
	}
	client := upstream.New(60 * time.Second)
	ctx := context.Background()

	type row struct {
		UID      string         `json:"uid"`
		UserName string         `json:"user_name"`
		Credits  int64          `json:"credits"`
		Apply    map[string]any `json:"apply,omitempty"`
		Error    string         `json:"error,omitempty"`
	}
	var rows []row
	for _, a := range auths {
		if *uid != "" && a.UID != *uid {
			continue
		}
		r := row{UID: a.UID, UserName: a.UserName}
		balance, err := client.CreditBalance(ctx, a.Token())
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Credits = upstream.ParseCredits(balance)
		}
		if *apply != "" {
			res := map[string]any{}
			switch *apply {
			case "register":
				reg, err := client.Register(ctx, a.Token())
				if err != nil {
					res["error"] = err.Error()
				} else {
					res["new_user"] = reg.NewUser
					res["bonus"] = reg.RegistrationBonus
					res["expire_days"] = reg.RewardExpireDays
					res["valid_until"] = reg.RewardLastValidDate
				}
			case "campaign":
				out, err := client.CampaignInit(ctx, a.Token())
				if err != nil {
					res["error"] = err.Error()
				} else {
					res["data"] = out
				}
			}
			r.Apply = res
		}
		rows = append(rows, r)
	}
	if *jsonOut {
		raw, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(raw))
		return
	}
	for _, r := range rows {
		if *pretty {
			fmt.Printf("CatPaw %s (%s)\n", r.UID, r.UserName)
			fmt.Printf("  credits: %d\n", r.Credits)
			if r.Error != "" {
				fmt.Printf("  error: %s\n", r.Error)
			}
			if r.Apply != nil {
				raw, _ := json.Marshal(r.Apply)
				fmt.Printf("  apply: %s\n", raw)
			}
			continue
		}
		fmt.Printf("%s (%s): credits=%d", r.UID, r.UserName, r.Credits)
		if r.Error != "" {
			fmt.Printf(" error=%s", r.Error)
		}
		if r.Apply != nil {
			fmt.Printf(" apply=%v", r.Apply)
		}
		fmt.Println()
	}
	if len(rows) == 0 {
		fmt.Println("no matching account")
	}
	_ = strings.TrimSpace
}
