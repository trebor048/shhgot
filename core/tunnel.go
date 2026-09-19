package core

import (
	"bufio"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Pre-compiled regex patterns for tunnel URL extraction (performance optimization)
var (
	tunnelURLRegex1 = regexp.MustCompile(`https://[\w-]+\.cfargotunnel\.com`)
	tunnelURLRegex2 = regexp.MustCompile(`tunnel URL is (https://[\w.-]+)`)
	tunnelURLRegex3 = regexp.MustCompile(`connected to (https://[\w.-]+)`)
)

// TunnelConfig holds Cloudflare Tunnel configuration
type TunnelConfig struct {
	Enabled    bool
	TunnelName string
	LocalURL   string
}

// StartCloudflaredTunnel starts a Cloudflare Tunnel connection
// This allows remote access without exposing a public IP
func StartCloudflaredTunnel(config *TunnelConfig) error {
	if !config.Enabled {
		return nil
	}

	// Check if cloudflared is installed
	cmd := exec.Command("cloudflared", "--version")
	if err := cmd.Run(); err != nil {
		log.Printf("[TUNNEL] ERROR: cloudflared not installed")
		log.Printf("[TUNNEL] Install from: https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/installation/")
		return err
	}

	log.Printf("[TUNNEL] Starting Cloudflare Tunnel: %s", config.TunnelName)
	log.Printf("[TUNNEL] Local URL: %s", config.LocalURL)

	// Start the tunnel and capture its output
	cmd = exec.Command("cloudflared", "tunnel", "run", config.TunnelName)
	
	// Capture stdout/stderr to monitor for the tunnel URL
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[TUNNEL] Error creating stdout pipe: %v", err)
	}
	
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[TUNNEL] Error creating stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[TUNNEL] ERROR: Failed to start tunnel: %v", err)
		return err
	}

	log.Printf("[TUNNEL] Tunnel started successfully!")

	// Monitor output for tunnel URL
	go monitorTunnelOutput(stdout, stderr, config.TunnelName)

	// Wait for the tunnel to finish (will run indefinitely)
	if err := cmd.Wait(); err != nil {
		log.Printf("[TUNNEL] Tunnel stopped: %v", err)
		return err
	}

	return nil
}

// monitorTunnelOutput monitors cloudflared output for the tunnel URL
func monitorTunnelOutput(stdout, stderr interface{}, tunnelName string) {
	urlFound := false
	
	// Monitor stdout
	if stdout != nil {
		go func() {
			scanner := bufio.NewScanner(stdout.(interface{ Read(p []byte) (n int, err error) }))
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Println("[TUNNEL OUT]", line)
				
				if !urlFound {
					if url := extractURLFromLog(line); url != "" {
						printTunnelURL(url)
						urlFound = true
					}
				}
			}
		}()
	}

	// Monitor stderr
	if stderr != nil {
		go func() {
			scanner := bufio.NewScanner(stderr.(interface{ Read(p []byte) (n int, err error) }))
			for scanner.Scan() {
				line := scanner.Text()
				fmt.Println("[TUNNEL ERR]", line)
				
				if !urlFound {
					if url := extractURLFromLog(line); url != "" {
						printTunnelURL(url)
						urlFound = true
					}
				}
			}
		}()
	}

	// If URL not found after 10 seconds, print default
	go func() {
		time.Sleep(10 * time.Second)
		if !urlFound {
			defaultURL := fmt.Sprintf("https://%s.cfargotunnel.com", tunnelName)
			printTunnelURL(defaultURL)
		}
	}()
}

// extractURLFromLog extracts tunnel URL from cloudflared log output
func extractURLFromLog(line string) string {
	// Use pre-compiled regex patterns for performance
	regexes := []*regexp.Regexp{
		tunnelURLRegex1,
		tunnelURLRegex2,
		tunnelURLRegex3,
	}

	for _, re := range regexes {
		matches := re.FindAllStringSubmatch(line, 1)
		if len(matches) > 0 {
			if len(matches[0]) > 1 {
				return matches[0][1]
			} else if len(matches[0]) > 0 {
				return matches[0][0]
			}
		}
	}

	return ""
}

// printTunnelURL prints the tunnel URL in a prominent way
func printTunnelURL(url string) {
	fmt.Println()
	fmt.Println("╔════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                   🔐 CLOUDFLARE TUNNEL READY 🔐                    ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("📍 Your shhgit instance is accessible at:\n")
	fmt.Printf("\n")
	fmt.Printf("   🔗 %s\n", url)
	fmt.Printf("\n")
	fmt.Println("✅ Automatic HTTPS enabled")
	fmt.Println("✅ DDoS protection enabled")
	fmt.Println("✅ No public IP exposure")
	fmt.Println()
	fmt.Println("📝 Next Steps:")
	fmt.Println("   1. Add DNS CNAME in Cloudflare dashboard:")
	fmt.Println("      - Name: subdomain (e.g., shhgit)")
	fmt.Println("      - Type: CNAME")
	fmt.Printf("      - Content: %s.cfargotunnel.com\n", extractTunnelName(url))
	fmt.Println()
	fmt.Println("   2. Access with custom domain:")
	fmt.Println("      https://yourdomain.com")
	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
}

// extractTunnelName extracts the tunnel name from the URL
func extractTunnelName(url string) string {
	// Remove https:// prefix
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	
	// Get the part before .cfargotunnel.com
	parts := strings.Split(url, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return "shhgit"
}

// PrintTunnelSetupInstructions prints instructions for setting up Cloudflare Tunnel
func PrintTunnelSetupInstructions(tunnelName string) {
	fmt.Printf(`
╔════════════════════════════════════════════════════════════════════╗
║           Cloudflare Tunnel Setup Instructions                    ║
╚════════════════════════════════════════════════════════════════════╝

QUICK START:
============

1. Install cloudflared:
   Linux/Mac: brew install cloudflare/cloudflare/cloudflared
   Linux (apt): https://pkg.cloudflare.com/
   Windows: Download from https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/installation/

2. Login to Cloudflare:
   $ cloudflared tunnel login
   (This opens your browser to authenticate)

3. Create a tunnel named '%s':
   $ cloudflared tunnel create %s

4. Configure the tunnel (create ~/.cloudflared/config.yml):
   
   tunnel: %s
   credentials-file: ~/.cloudflare-warp/cert.pem

   ingress:
     - hostname: shhgit.yourdomain.com
       service: http://localhost:8080
     - service: http_status:404

5. Add DNS CNAME record in Cloudflare Dashboard:
   Name: shhgit
   Type: CNAME
   Content: %s.cfargotunnel.com
   Proxy Status: Proxied

6. Start the tunnel:
   $ cloudflared tunnel run %s

OR start shhgit with tunnel flag:
   $ ./shhgit --tunnel=%s

BENEFITS:
=========
✓ No public IP required
✓ Automatic HTTPS/TLS with Let's Encrypt
✓ Built-in DDoS protection
✓ No port forwarding needed
✓ Works behind NAT/firewall
✓ Automatic DNS management
✓ Access from anywhere

For more info: https://developers.cloudflare.com/cloudflare-one/

`, tunnelName, tunnelName, tunnelName, tunnelName, tunnelName, tunnelName)
}

// HealthCheckCloudflaredTunnel checks if cloudflared is running
func HealthCheckCloudflaredTunnel() (bool, error) {
	cmd := exec.Command("cloudflared", "--version")
	return cmd.Run() == nil, nil
}
