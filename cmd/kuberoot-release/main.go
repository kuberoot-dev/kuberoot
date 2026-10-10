// Command kuberoot-release makes release keys and signs release bundles.
//
//	kuberoot-release keygen <prefix>        writes <prefix>.key and <prefix>.pub
//	kuberoot-release sign <bundle> <key>    writes <bundle>.sig
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/kuberoot-dev/kuberoot/pkg/release"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 3 {
		log.Fatal("usage: kuberoot-release keygen <prefix> | sign <bundle> <key>")
	}
	switch os.Args[1] {
	case "keygen":
		priv, pub, err := release.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(os.Args[2]+".key", priv, 0o600); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(os.Args[2]+".pub", pub, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %[1]s.key (keep it secret) and %[1]s.pub\n", os.Args[2])
	case "sign":
		if len(os.Args) != 4 {
			log.Fatal("usage: kuberoot-release sign <bundle> <key>")
		}
		key, err := os.ReadFile(os.Args[3])
		if err != nil {
			log.Fatal(err)
		}
		sum, err := release.FileSum(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		sig, err := release.Sign(sum, key)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(os.Args[2]+".sig", []byte(sig+"\n"), 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("signed %s\n", os.Args[2])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}
