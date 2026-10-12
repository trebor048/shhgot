package core

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/fatih/color"
)

const (
	Name    = "shhgit"
	Version = "0.4"
	Author  = "Paul Price (@darkp0rt) - www.darkport.co.uk || LuckyCanucky - github.com/trebor048"
)

const BannerText = `███████╗██╗  ██╗██╗  ██╗ ██████╗  ██████╗ ████████╗         
██╔════╝██║  ██║██║  ██║██╔════╝ ██╔═══██╗╚══██╔══╝         
███████╗███████║███████║██║  ███╗██║   ██║   ██║            
╚════██║██╔══██║██╔══██║██║   ██║██║   ██║   ██║            
███████║██║  ██║██║  ██║╚██████╔╝╚██████╔╝   ██║            
╚══════╝╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝  ╚═════╝    ╚═╝            
                                                            
███████╗███████╗ ██████╗██████╗ ███████╗████████╗           
██╔════╝██╔════╝██╔════╝██╔══██╗██╔════╝╚══██╔══╝           
███████╗█████╗  ██║     ██████╔╝█████╗     ██║              
╚════██║██╔══╝  ██║     ██╔══██╗██╔══╝     ██║              
███████║███████╗╚██████╗██║  ██║███████╗   ██║              
╚══════╝╚══════╝ ╚═════╝╚═╝  ╚═╝╚══════╝   ╚═╝              
                                                            
███████╗ ██████╗ █████╗ ███╗   ██╗███╗   ██╗███████╗██████╗ 
██╔════╝██╔════╝██╔══██╗████╗  ██║████╗  ██║██╔════╝██╔══██╗
███████╗██║     ███████║██╔██╗ ██║██╔██╗ ██║█████╗  ██████╔╝
╚════██║██║     ██╔══██║██║╚██╗██║██║╚██╗██║██╔══╝  ██╔══██╗
███████║╚██████╗██║  ██║██║ ╚████║██║ ╚████║███████╗██║  ██║
╚══════╝ ╚═════╝╚═╝  ╚═╝╚═╝  ╚═══╝╚═╝  ╚═══╝╚══════╝╚═╝  ╚═╝
`

const Banner = BannerText + "    v" + Version

// ColorGradient represents a 3-color gradient
type ColorGradient struct {
	Colors [3]*color.Color
}

// Pre-defined color gradients for variety
var colorGradients = []ColorGradient{
	// Sunset
	{Colors: [3]*color.Color{
		color.New(color.FgHiRed),
		color.New(color.FgHiYellow),
		color.New(color.FgHiMagenta),
	}},
	// Ocean
	{Colors: [3]*color.Color{
		color.New(color.FgHiCyan),
		color.New(color.FgHiBlue),
		color.New(color.FgHiMagenta),
	}},
	// Forest
	{Colors: [3]*color.Color{
		color.New(color.FgHiGreen),
		color.New(color.FgHiCyan),
		color.New(color.FgHiBlue),
	}},
	// Fire
	{Colors: [3]*color.Color{
		color.New(color.FgHiYellow),
		color.New(color.FgHiRed),
		color.New(color.FgHiMagenta),
	}},
	// Purple Dream
	{Colors: [3]*color.Color{
		color.New(color.FgHiMagenta),
		color.New(color.FgHiBlue),
		color.New(color.FgHiCyan),
	}},
	// Candy
	{Colors: [3]*color.Color{
		color.New(color.FgHiMagenta),
		color.New(color.FgHiYellow),
		color.New(color.FgHiCyan),
	}},
	// Matrix
	{Colors: [3]*color.Color{
		color.New(color.FgHiGreen),
		color.New(color.FgGreen),
		color.New(color.FgHiCyan),
	}},
	// Neon
	{Colors: [3]*color.Color{
		color.New(color.FgHiRed),
		color.New(color.FgHiCyan),
		color.New(color.FgHiYellow),
	}},
}

// bannerDelay is the per-character typing pace for the startup banner. It is
// deliberately slower than the 1 ms decoration pace: the banner is the
// operator's startup readout, not wallpaper. When the typewriter effect is
// enabled with a slower configured delay, that delay wins instead.
func bannerDelay() time.Duration {
	const defaultBannerPace = 5 * time.Millisecond
	if session != nil && session.Config != nil &&
		session.Config.TypewriterEffect &&
		session.Config.TypewriterDelayMS > 5 {
		return time.Duration(session.Config.TypewriterDelayMS) * time.Millisecond
	}
	return defaultBannerPace
}

// PrintAnimatedBanner displays the banner with animated typing and random gradient
func PrintAnimatedBanner() {
	// Pick a random gradient. The global source is auto-seeded since Go 1.20,
	// so rand.Seed here is unnecessary (and deprecated).
	gradient := colorGradients[rand.Intn(len(colorGradients))]

	lines := splitLines(BannerText)
	totalLines := len(lines)

	// Calculate which color to use for each line (gradient distribution)
	for i, line := range lines {
		// Determine color based on position in gradient
		colorIndex := (i * 3) / totalLines
		if colorIndex > 2 {
			colorIndex = 2
		}
		colorFunc := gradient.Colors[colorIndex]

		// Type out each character
		for _, char := range line {
			colorFunc.Print(string(char))
			time.Sleep(bannerDelay())
		}
		fmt.Println() // Newline after each line
	}

	// Print version on last line with middle gradient color
	gradient.Colors[1].Printf("    v%s\n", Version)
}

// splitLines splits text into individual lines
func splitLines(text string) []string {
	var lines []string
	var currentLine string

	for _, char := range text {
		if char == '\n' {
			lines = append(lines, currentLine)
			currentLine = ""
		} else {
			currentLine += string(char)
		}
	}

	// Add last line if not empty
	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return lines
}

// TypePrint prints text with typing animation if enabled in config
func TypePrint(text string) {
	if session == nil || session.Config == nil || !session.Config.TypewriterEffect {
		// Typewriter disabled, print normally
		fmt.Print(text)
		return
	}

	delay := time.Millisecond * 1
	if session.Config.TypewriterDelayMS > 0 {
		delay = time.Duration(session.Config.TypewriterDelayMS) * time.Millisecond
	}

	for _, char := range text {
		fmt.Print(string(char))
		time.Sleep(delay)
	}
}
