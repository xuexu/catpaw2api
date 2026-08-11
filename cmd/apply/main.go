// apply 工具：批量自动申请额度（对应其他项目的自动签到）。
// 遍历 auths/ 下全部账号，低于阈值自动申请额度。
// 用法: apply [-auth-dir ./auths] [-method register|campaign] [-threshold 50] [-force]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"catpaw2api/internal/auth"
	"catpaw2api/internal/upstream"
)

func main() {
	authDir := flag.String("auth-dir", "./auths", "auth dir")
	method := flag.String("method", "register", "apply method: register | campaign")
	threshold := flag.Int64("threshold", 50, "apply when credits below this value")
	force := flag.Bool("force", false, "apply even if credits above threshold")
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

	applied := 0
	for _, a := range auths {
		balance, err := client.CreditBalance(ctx, a.Token())
		if err != nil {
			fmt.Printf("%s: balance error: %v\n", a.UID, err)
			continue
		}
		credits := upstream.ParseCredits(balance)
		if credits >= *threshold && !*force {
			fmt.Printf("%s: credits=%d >= threshold=%d, skip\n", a.UID, credits, *threshold)
			continue
		}
		fmt.Printf("%s: credits=%d < threshold=%d, applying (%s) ...\n", a.UID, credits, *threshold, *method)
		switch *method {
		case "register":
			reg, err := client.Register(ctx, a.Token())
			if err != nil {
				fmt.Printf("  register error: %v\n", err)
				continue
			}
			fmt.Printf("  newUser=%v bonus=%d expireDays=%d validUntil=%s\n",
				reg.NewUser, reg.RegistrationBonus, reg.RewardExpireDays, reg.RewardLastValidDate)
		case "campaign":
			out, err := client.CampaignInit(ctx, a.Token())
			if err != nil {
				fmt.Printf("  campaign error: %v\n", err)
				continue
			}
			fmt.Printf("  campaign: %v\n", out)
		default:
			fmt.Printf("  unknown method %q\n", *method)
			continue
		}
		applied++
	}
	fmt.Printf("done, applied=%d/%d\n", applied, len(auths))
}
