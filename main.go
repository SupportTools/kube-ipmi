package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// -------------------- Data Structures --------------------

type SystemInfo struct {
	Manufacturer    string
	Model           string
	SerialNumber    string
	IPAddress       string
	MACAddress      string
	DellServiceTag  string
	DellExpressCode string
}

// -------------------- IPMI Parsing --------------------

func parseFruPrint(output string) (manufacturer, model, serial string) {
	reMfg := regexp.MustCompile(`(?i)Board Mfg\s*:\s*(.*)`)
	reModel := regexp.MustCompile(`(?i)(Board Product|Product Name)\s*:\s*(.*)`)
	reSerial := regexp.MustCompile(`(?i)(Board Serial|Product Serial)\s*:\s*(.*)`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reMfg.FindStringSubmatch(line); m != nil {
			manufacturer = strings.TrimSpace(m[1])
		}
		if m := reModel.FindStringSubmatch(line); m != nil {
			if model == "" {
				model = strings.TrimSpace(m[2])
			}
		}
		if m := reSerial.FindStringSubmatch(line); m != nil {
			if serial == "" {
				serial = strings.TrimSpace(m[2])
			}
		}
	}
	return
}

func parseLanPrint(output string) (ipAddress, macAddress string) {
	// This matches lines like:
	// "IP Address       : 192.168.1.100"
	// "MAC Address      : 78:45:c4:f3:17:49"
	reIP := regexp.MustCompile(`(?i)IP Address\s*:\s*([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
	reMAC := regexp.MustCompile(`(?i)MAC Address\s*:\s*([0-9A-Fa-f:]+)`)

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reIP.FindStringSubmatch(line); m != nil {
			ipAddress = strings.TrimSpace(m[1])
		}
		if m := reMAC.FindStringSubmatch(line); m != nil {
			macAddress = strings.TrimSpace(m[1])
		}
	}
	return
}

// -------------------- DMI Parsing --------------------

func dellServiceTagToExpressCode(tag string) string {
	s := strings.ToUpper(tag)
	if len(s) < 5 || len(s) > 10 {
		return ""
	}
	val, err := strconv.ParseUint(s, 36, 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d", val)
}

func parseDmiSystem(dmiOutput string) (manufacturer, productName, serial string) {
	reMfg := regexp.MustCompile(`(?i)Manufacturer:\s*(.*)`)
	reProduct := regexp.MustCompile(`(?i)Product Name:\s*(.*)`)
	reSerial := regexp.MustCompile(`(?i)Serial Number:\s*(.*)`)

	scanner := bufio.NewScanner(strings.NewReader(dmiOutput))
	for scanner.Scan() {
		line := scanner.Text()
		if m := reMfg.FindStringSubmatch(line); m != nil {
			manufacturer = strings.TrimSpace(m[1])
		}
		if m := reProduct.FindStringSubmatch(line); m != nil {
			productName = strings.TrimSpace(m[1])
		}
		if m := reSerial.FindStringSubmatch(line); m != nil {
			serial = strings.TrimSpace(m[1])
		}
	}
	return
}

// -------------------- Main --------------------

func main() {
	ctx := context.Background()

	// 1) Gather IPMI FRU data.
	fruCmd := exec.Command("ipmitool", "fru", "print")
	fruOut, err := fruCmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to run 'ipmitool fru print': %v\nOutput: %s", err, string(fruOut))
	}
	manufacturer, model, serial := parseFruPrint(string(fruOut))

	// 2) Gather IPMI LAN data (on channel "1").
	lanCmd := exec.Command("ipmitool", "lan", "print", "1")
	lanOut, lanErr := lanCmd.CombinedOutput()

	if lanErr != nil {
		// Log the error and the entire output
		log.Printf("WARNING: 'ipmitool lan print 1' returned error: %v", lanErr)
		log.Printf("DEBUG: lan print output:\n%s", string(lanOut))
	} else {
		log.Printf("DEBUG: 'ipmitool lan print 1' output:\n%s", string(lanOut))
	}

	ipAddr, macAddr := parseLanPrint(string(lanOut))
	log.Printf("DEBUG: Parsed IP '%s' and MAC '%s' from the above output", ipAddr, macAddr)

	// Build the initial SystemInfo from IPMI
	sysInfo := SystemInfo{
		Manufacturer: manufacturer,
		Model:        model,
		SerialNumber: serial,
		IPAddress:    ipAddr,
		MACAddress:   macAddr,
	}

	// 3) Optionally override with dmidecode (local OS) if needed
	dmiCmd := exec.Command("dmidecode", "--type", "system")
	var dmiBuf bytes.Buffer
	dmiCmd.Stdout = &dmiBuf
	if err := dmiCmd.Run(); err != nil {
		log.Printf("Failed to run dmidecode: %v", err)
	} else {
		dmiMfg, dmiProduct, dmiSerial := parseDmiSystem(dmiBuf.String())

		if dmiMfg != "" && (sysInfo.Manufacturer == "" || len(sysInfo.Manufacturer) < len(dmiMfg)) {
			sysInfo.Manufacturer = dmiMfg
		}
		if dmiProduct != "" && (sysInfo.Model == "" || len(sysInfo.Model) < len(dmiProduct)) {
			sysInfo.Model = dmiProduct
		}
		if dmiSerial != "" && dmiSerial != sysInfo.SerialNumber {
			sysInfo.SerialNumber = dmiSerial
			// If it's Dell, set Dell fields
			if strings.Contains(strings.ToLower(sysInfo.Manufacturer), "dell") {
				sysInfo.DellServiceTag = dmiSerial
				sysInfo.DellExpressCode = dellServiceTagToExpressCode(dmiSerial)
			}
		}
	}

	// If it's Dell, set final serial to the short service tag
	if strings.Contains(strings.ToLower(sysInfo.Manufacturer), "dell") && sysInfo.DellServiceTag != "" {
		sysInfo.SerialNumber = sysInfo.DellServiceTag
	}

	// 4) Print final results (for debugging/logging)
	log.Println("===== IPMI + DMI System Information =====")
	log.Printf("Manufacturer        : %s\n", sysInfo.Manufacturer)
	log.Printf("Model               : %s\n", sysInfo.Model)
	log.Printf("SerialNumber        : %s\n", sysInfo.SerialNumber)
	if strings.Contains(strings.ToLower(sysInfo.Manufacturer), "dell") {
		log.Printf("Dell Express Code   : %s\n", sysInfo.DellExpressCode)
	}
	log.Printf("IP Address          : %s\n", sysInfo.IPAddress)
	log.Printf("MAC Address         : %s\n", sysInfo.MACAddress)

	// 5) Update the Node object with annotations
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Println("NODE_NAME environment variable is not set. Cannot annotate node.")
		return
	}

	// Setup in-cluster config and client
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Failed to get in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	// Get the Node object
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Failed to get node %s: %v", nodeName, err)
	}

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations["ipmi.support.tools/manufacturer"] = sysInfo.Manufacturer
	node.Annotations["ipmi.support.tools/model"] = sysInfo.Model
	node.Annotations["ipmi.support.tools/serial-number"] = sysInfo.SerialNumber
	node.Annotations["ipmi.support.tools/ip-address"] = sysInfo.IPAddress
	node.Annotations["ipmi.support.tools/mac-address"] = sysInfo.MACAddress

	if sysInfo.DellServiceTag != "" {
		node.Annotations["ipmi.support.tools/dell-service-tag"] = sysInfo.DellServiceTag
	}
	if sysInfo.DellExpressCode != "" {
		node.Annotations["ipmi.support.tools/dell-express-code"] = sysInfo.DellExpressCode
	}

	_, err = clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		log.Fatalf("Failed to update node annotations: %v", err)
	}
	log.Printf("Successfully updated Node %q annotations.", nodeName)
}
