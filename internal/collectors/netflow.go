package collectors

import (
	"argus/internal/fields"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"  // Import for io.Writer
	"log" // Import for logging
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

type Frontend interface {
	Start()
	Configure(config map[string]string)
}

// CONSTANTS (remain the same, as they are imported from fields.go)
const IN_BYTES = fields.IN_BYTES
const IN_PKTS = fields.IN_PKTS
const FLOWS = fields.FLOWS
const PROTOCOL = fields.PROTOCOL
const SRC_TOS = fields.SRC_TOS
const TCP_FLAGS = fields.TCP_FLAGS
const L4_SRC_PORT = fields.L4_SRC_PORT
const IPV4_SRC_ADDR = fields.IPV4_SRC_ADDR
const SRC_MASK = fields.SRC_MASK
const INPUT_SNMP = fields.INPUT_SNMP
const L4_DST_PORT = fields.L4_DST_PORT
const IPV4_DST_ADDR = fields.IPV4_DST_ADDR
const DST_MASK = fields.DST_MASK
const OUTPUT_SNMP = fields.OUTPUT_SNMP
const IPV4_NEXT_HOP = fields.IPV4_NEXT_HOP
const OUT_BYTES = fields.OUT_BYTES
const OUT_PKTS = fields.OUT_PKTS
const LAST_SWITCHED = fields.LAST_SWITCHED
const IPV6_SRC_ADDR = fields.IPV6_SRC_ADDR
const IPV6_DST_ADDR = fields.IPV6_DST_ADDR

// New fields added (from fields.go)
const SRC_AS = fields.SRC_AS
const DST_AS = fields.DST_AS
const BGP_IPV4_NEXT_HOP = fields.BGP_IPV4_NEXT_HOP
const FIRST_SWITCHED = fields.FIRST_SWITCHED
const MIN_PKT_LNGTH = fields.MIN_PKT_LNGTH
const MAX_PKT_LNGTH = fields.MAX_PKT_LNGTH
const MIN_IP_TTL = fields.MIN_IP_TTL // Now correctly aliased in fields.go
const MAX_IP_TTL = fields.MAX_IP_TTL // Now correctly aliased in fields.go
const SAMPLING_INTERVAL = fields.SAMPLING_INTERVAL
const ICMP_TYPE = fields.ICMP_TYPE
const MUL_IGMP_TYPE = fields.MUL_IGMP_TYPE
const SAMPLING_ALGORITHM = fields.SAMPLING_ALGORITHM
const FLOW_ACTIVE_TIMEOUT = fields.FLOW_ACTIVE_TIMEOUT
const FLOW_INACTIVE_TIMEOUT = fields.FLOW_INACTIVE_TIMEOUT
const ENGINE_TYPE = fields.ENGINE_TYPE
const ENGINE_ID = fields.ENGINE_ID
const TOTAL_BYTES_EXP = fields.TOTAL_BYTES_EXP
const TOTAL_PKTS_EXP = fields.TOTAL_PKTS_EXP
const TOTAL_FLOWS_EXP = fields.TOTAL_FLOWS_EXP
const IPV4_SRC_PREFIX = fields.IPV4_SRC_PREFIX
const IPV4_DST_PREFIX = fields.IPV4_DST_PREFIX
const MPLS_TOP_LABEL_TYPE = fields.MPLS_TOP_LABEL_TYPE
const MPLS_TOP_LABEL_IP_ADDR = fields.MPLS_TOP_LABEL_IP_ADDR
const FLOW_SAMPLER_ID = fields.FLOW_SAMPLER_ID
const FLOW_SAMPLER_MODE = fields.FLOW_SAMPLER_MODE
const FLOW_SAMPLER_RANDOM_INTERVAL = fields.FLOW_SAMPLER_RANDOM_INTERVAL
const MIN_TTL = fields.MIN_TTL
const MAX_TTL = fields.MAX_TTL
const IPV4_IDENT = fields.IPV4_IDENT
const DST_TOS = fields.DST_TOS
const IN_SRC_MAC = fields.IN_SRC_MAC
const OUT_DST_MAC = fields.OUT_DST_MAC
const SRC_VLAN = fields.SRC_VLAN
const DST_VLAN = fields.DST_VLAN
const IP_PROTOCOL_VERSION = fields.IP_PROTOCOL_VERSION
const DIRECTION = fields.DIRECTION
const IPV6_NEXT_HOP = fields.IPV6_NEXT_HOP
const BPG_IPV6_NEXT_HOP = fields.BPG_IPV6_NEXT_HOP
const IPV6_OPTION_HEADERS = fields.IPV6_OPTION_HEADERS
const MPLS_LABEL_1 = fields.MPLS_LABEL_1
const MPLS_LABEL_2 = fields.MPLS_LABEL_2
const MPLS_LABEL_3 = fields.MPLS_LABEL_3
const MPLS_LABEL_4 = fields.MPLS_LABEL_4
const MPLS_LABEL_5 = fields.MPLS_LABEL_5
const MPLS_LABEL_6 = fields.MPLS_LABEL_6
const MPLS_LABEL_7 = fields.MPLS_LABEL_7
const MPLS_LABEL_8 = fields.MPLS_LABEL_8
const MPLS_LABEL_9 = fields.MPLS_LABEL_9
const MPLS_LABEL_10 = fields.MPLS_LABEL_10
const IN_DST_MAC = fields.IN_DST_MAC
const OUT_SRC_MAC = fields.OUT_SRC_MAC
const IF_NAME = fields.IF_NAME
const IF_DESC = fields.IF_DESC
const SAMPLER_NAME = fields.SAMPLER_NAME
const IN_PERMANENT_BYTES = fields.IN_PERMANENT_BYTES
const IN_PERMANENT_PKTS = fields.IN_PERMANENT_PKTS
const FRAGMENT_OFFSET = fields.FRAGMENT_OFFSET
const FORWARDING_STATUS = fields.FORWARDING_STATUS
const MPLS_PAL_RD = fields.MPLS_PAL_RD
const MPLS_PREFIX_LEN = fields.MPLS_PREFIX_LEN
const SRC_TRAFFIC_INDEX = fields.SRC_TRAFFIC_INDEX
const DST_TRAFFIC_INDEX = fields.DST_TRAFFIC_INDEX
const APPLICATION_DESCRIPTION = fields.APPLICATION_DESCRIPTION
const APPLICATION_TAG = fields.APPLICATION_TAG
const APPLICATION_NAME = fields.APPLICATION_NAME
const POST_IP_DIFF_SERV_CODE_POINT = fields.POST_IP_DIFF_SERV_CODE_POINT
const REPLICATION_FACTOR = fields.REPLICATION_FACTOR
const DEPRECATED = fields.DEPRECATED
const LAYER2_PACKET_SECTION_OFFSET = fields.LAYER2_PACKET_SECTION_OFFSET
const LAYER2_PACKET_SECTION_SIZE = fields.LAYER2_PACKET_SECTION_SIZE
const LAYER2_PACKET_SECTION_DATA = fields.LAYER2_PACKET_SECTION_DATA
const FIREWALL_EVENT = fields.FIREWALL_EVENT

const CUSTOM_TIMESTAMP_START uint16 = 65001

// MikroTik/IPFIX Custom Fields (NAT related)
const POST_NAT_SOURCE_IPV4_ADDRESS = fields.POST_NAT_SOURCE_IPV4_ADDRESS
const POST_NAT_DESTINATION_IPV4_ADDRESS = fields.POST_NAT_DESTINATION_IPV4_ADDRESS
const POST_NAPT_SOURCE_TRANSPORT_PORT = fields.POST_NAPT_SOURCE_TRANSPORT_PORT
const POST_NAPT_DESTINATION_TRANSPORT_PORT = fields.POST_NAPT_DESTINATION_TRANSPORT_PORT

const ROUTER_TEMPLATE_PKTS_ID = 3
const ROUTER_TEMPLATE_BYTES_ID = 4
const ROUTER_TEMPLATE_LAST_SWITCHED_ID = 1
const ROUTER_TEMPLATE_FIRST_SWITCHED_ID = 2

// Extension fields
const _TIMESTAMP = fields.CUSTOM_TIMESTAMP // Now using your new custom timestamp ID

var FUNCTIONMAP = map[uint16]func([]byte) fields.Value{
	IN_BYTES:                             fields.GetInt,
	IN_PKTS:                              fields.GetInt,
	FLOWS:                                fields.GetInt,
	PROTOCOL:                             fields.GetInt,
	SRC_TOS:                              fields.GetInt,
	TCP_FLAGS:                            fields.GetInt,
	L4_SRC_PORT:                          fields.GetInt,
	IPV4_SRC_ADDR:                        fields.GetAddr,
	SRC_MASK:                             fields.GetInt,
	INPUT_SNMP:                           fields.GetInt,
	L4_DST_PORT:                          fields.GetInt,
	IPV4_DST_ADDR:                        fields.GetAddr,
	DST_MASK:                             fields.GetInt,
	OUTPUT_SNMP:                          fields.GetInt,
	IPV4_NEXT_HOP:                        fields.GetAddr,
	OUT_BYTES:                            fields.GetInt,
	OUT_PKTS:                             fields.GetInt,
	LAST_SWITCHED:                        fields.GetInt,
	IPV6_SRC_ADDR:                        fields.GetAddr6,
	IPV6_DST_ADDR:                        fields.GetAddr6,
	SRC_AS:                               fields.GetInt,
	DST_AS:                               fields.GetInt,
	BGP_IPV4_NEXT_HOP:                    fields.GetAddr,
	FIRST_SWITCHED:                       fields.GetInt,
	MIN_PKT_LNGTH:                        fields.GetInt,
	MAX_PKT_LNGTH:                        fields.GetInt,
	MIN_TTL:                              fields.GetInt,
	MAX_TTL:                              fields.GetInt,
	SAMPLING_INTERVAL:                    fields.GetInt,
	ICMP_TYPE:                            fields.GetInt,
	MUL_IGMP_TYPE:                        fields.GetInt,
	SAMPLING_ALGORITHM:                   fields.GetInt,
	FLOW_ACTIVE_TIMEOUT:                  fields.GetInt,
	FLOW_INACTIVE_TIMEOUT:                fields.GetInt,
	ENGINE_TYPE:                          fields.GetInt,
	ENGINE_ID:                            fields.GetInt,
	TOTAL_BYTES_EXP:                      fields.GetInt,
	TOTAL_PKTS_EXP:                       fields.GetInt,
	TOTAL_FLOWS_EXP:                      fields.GetInt,
	IPV4_SRC_PREFIX:                      fields.GetAddr,
	IPV4_DST_PREFIX:                      fields.GetAddr,
	MPLS_TOP_LABEL_TYPE:                  fields.GetInt,
	MPLS_TOP_LABEL_IP_ADDR:               fields.GetAddr,
	FLOW_SAMPLER_ID:                      fields.GetInt,
	FLOW_SAMPLER_MODE:                    fields.GetInt,
	FLOW_SAMPLER_RANDOM_INTERVAL:         fields.GetInt,
	IPV4_IDENT:                           fields.GetInt,
	DST_TOS:                              fields.GetInt,
	IN_SRC_MAC:                           fields.GetMac,
	OUT_DST_MAC:                          fields.GetMac,
	SRC_VLAN:                             fields.GetInt,
	DST_VLAN:                             fields.GetInt,
	IP_PROTOCOL_VERSION:                  fields.GetInt,
	DIRECTION:                            fields.GetInt,
	IPV6_NEXT_HOP:                        fields.GetAddr6,
	BPG_IPV6_NEXT_HOP:                    fields.GetAddr6,
	IPV6_OPTION_HEADERS:                  fields.GetInt,
	MPLS_LABEL_1:                         fields.GetInt,
	MPLS_LABEL_2:                         fields.GetInt,
	MPLS_LABEL_3:                         fields.GetInt,
	MPLS_LABEL_4:                         fields.GetInt,
	MPLS_LABEL_5:                         fields.GetInt,
	MPLS_LABEL_6:                         fields.GetInt,
	MPLS_LABEL_7:                         fields.GetInt,
	MPLS_LABEL_8:                         fields.GetInt,
	MPLS_LABEL_9:                         fields.GetInt,
	MPLS_LABEL_10:                        fields.GetInt,
	IN_DST_MAC:                           fields.GetMac,
	OUT_SRC_MAC:                          fields.GetMac,
	IF_NAME:                              fields.GetString,
	IF_DESC:                              fields.GetString,
	SAMPLER_NAME:                         fields.GetString,
	IN_PERMANENT_BYTES:                   fields.GetInt,
	IN_PERMANENT_PKTS:                    fields.GetInt,
	FRAGMENT_OFFSET:                      fields.GetInt,
	FORWARDING_STATUS:                    fields.GetInt,
	MPLS_PAL_RD:                          fields.GetBytes,
	MPLS_PREFIX_LEN:                      fields.GetInt,
	SRC_TRAFFIC_INDEX:                    fields.GetInt,
	DST_TRAFFIC_INDEX:                    fields.GetInt,
	APPLICATION_DESCRIPTION:              fields.GetString,
	APPLICATION_TAG:                      fields.GetInt,
	APPLICATION_NAME:                     fields.GetString,
	POST_IP_DIFF_SERV_CODE_POINT:         fields.GetInt,
	REPLICATION_FACTOR:                   fields.GetInt,
	DEPRECATED:                           fields.GetBytes,
	LAYER2_PACKET_SECTION_OFFSET:         fields.GetInt,
	LAYER2_PACKET_SECTION_SIZE:           fields.GetInt,
	LAYER2_PACKET_SECTION_DATA:           fields.GetBytes,
	FIREWALL_EVENT:                       fields.GetInt,
	POST_NAT_SOURCE_IPV4_ADDRESS:         fields.GetAddr,
	POST_NAT_DESTINATION_IPV4_ADDRESS:    fields.GetAddr,
	POST_NAPT_SOURCE_TRANSPORT_PORT:      fields.GetInt,
	POST_NAPT_DESTINATION_TRANSPORT_PORT: fields.GetInt,
	_TIMESTAMP:                           fields.GetInt,
}

// GENERICS
//
// Netflow listener and main object
type Netflow struct {
	Templates           map[uint32]map[uint32]map[uint16]netflowPacketTemplate
	BindAddr            net.IP
	BindPort            int
	debug               bool
	FlowChannel         chan map[uint16]fields.Value
	logger              *log.Logger // New logger field
	warningLogger       *log.Logger // Rate-limited diagnostics, independent of per-flow debug output.
	logFile             *os.File    // To hold the file handle if logging to file
	ReportedVersionOnce sync.Once
	ReportedVersion     uint16
}
type netflowPacket struct {
	Source    uint32
	Header    netflowPacketHeader
	Length    int
	Templates map[uint32]map[uint32]map[uint16]netflowPacketTemplate
	Data      []netflowDataFlowset
}
type netflowPacketHeader struct {
	Version             uint16
	Length              uint16 // <--- THIS WAS MISSING
	Count               uint16
	Uptime              uint32
	Usecs               uint32
	Sequence            uint32
	Id                  uint32 // v9: Engine ID, v10: ObservationDomainID
	ObservationDomainID uint32 // ipfix
	ExportTime          uint32 // ipfix
}
type netflowPacketFlowset struct {
	FlowSetID uint16
	Length    uint16
}

// TEMPLATE STRUCTS (remain the same)
type netflowPacketTemplate struct {
	Version     uint16
	Options     bool // Tombstone: recognize the ID without decoding options as flow data.
	FlowSetID   uint16
	Length      uint16
	ID          uint16
	FieldCount  uint16
	Fields      []templateField
	FieldLength int // Minimum encoded record size, including one-byte variable-length prefixes.
}
type templateField struct {
	FieldType  uint16
	Length     uint16
	Enterprise bool // IPFIX enterprise bit, including PEN zero: never use IANA decoders.
}

// DATA STRUCTS (remain the same)
type netflowDataFlowset struct {
	FlowSetID uint16
	Length    uint16
	Records   []flowRecord
}
type flowRecord struct {
	Values    []fields.Value
	ValuesMap map[uint16]fields.Value
}

//func (r *flowRecord) calcTime(s uint32, u uint32) uint32 {
//	var ts uint32
//
//	if flowendSecs, ok := r.ValuesMap[LAST_SWITCHED]; ok {
//		ts = u - (s / 1000) + (uint32(flowendSecs.ToInt()) / 1000)
//		v := fields.IntValue{Data: int(ts)}
//		r.ValuesMap[fields.CUSTOM_TIMESTAMP] = v // Use CUSTOM_TIMESTAMP here
//	}
//	return ts
//}

// netflow.go
// s = sysUpTime (ms since boot), u = unix seconds (seconds since epoch)
func (r *flowRecord) calcTime(s uint32, u uint32) uint32 {
	const msDiv = 1000

	// Προστασία σε ρολόγια που «γράφουν» ανάποδα (σπάνιο, αλλά ας υπάρχει)
	if u < (s / msDiv) {
		return 0
	}

	// Epoch του boot σε seconds
	base := u - (s / msDiv)

	var start, end uint32

	// FIRST_SWITCHED (ms since boot) -> start (epoch seconds)
	if v, ok := r.ValuesMap[FIRST_SWITCHED]; ok {
		start = base + (uint32(v.ToInt()) / msDiv)
		r.ValuesMap[fields.CUSTOM_TIMESTAMP_START] = fields.IntValue{Data: int(start)}
	}

	// LAST_SWITCHED (ms since boot) -> end (epoch seconds)
	if v, ok := r.ValuesMap[LAST_SWITCHED]; ok {
		end = base + (uint32(v.ToInt()) / msDiv)
		r.ValuesMap[fields.CUSTOM_TIMESTAMP] = fields.IntValue{Data: int(end)} // end
	}

	// Fallbacks: αν λείπει το ένα, καθρέφτισέ το από το άλλο
	if start == 0 && end != 0 {
		start = end
		r.ValuesMap[fields.CUSTOM_TIMESTAMP_START] = fields.IntValue{Data: int(start)}
	} else if end == 0 && start != 0 {
		end = start
		r.ValuesMap[fields.CUSTOM_TIMESTAMP] = fields.IntValue{Data: int(end)}
	}

	// Επιστρέφουμε το end όπως και πριν (για συμβατότητα)
	return end
}

func (r flowRecord) toString() string {
	var sl []string
	for _, v := range r.Values {
		sl = append(sl, v.ToString())
	}
	return strings.Join(sl, " : ") + "\n"
}

//calculate time for ipfix
//func (r *record) calcTimeFromExportTime(exportTime uint32) {
//	ts := time.Unix(int64(exportTime), 0).UTC()
//	r.ValuesMap[TypeTimeStart] = netflowValue{Raw: ts}
//	r.ValuesMap[TypeTimeEnd] = netflowValue{Raw: ts}
//}

/*
ParseData

Takes a slice of a data flowset and retreives all the flow records
Requires

	n netflowPacket : Netflow packet struct (for templates)
	p []byte : Data Flowset slice
	logger *log.Logger : Logger instance for warnings
*/
func parseData(n netflowPacket, p []byte, logger *log.Logger) netflowDataFlowset {
	var d netflowDataFlowset
	if len(p) < 4 {
		return d
	}
	d.FlowSetID = binary.BigEndian.Uint16(p)
	d.Length = binary.BigEndian.Uint16(p[2:])
	if int(d.Length) != len(p) {
		return d
	}
	t, ok := n.Templates[n.Source][n.Header.Id][d.FlowSetID]
	if !ok || t.Version != n.Header.Version || t.Options || t.FieldLength <= 0 || len(t.Fields) == 0 {
		if logger != nil {
			logger.Print("Skipping data set without an ordinary flow template")
		}
		return d
	}
	p = p[4:]
	malformed := func() netflowDataFlowset {
		if logger != nil {
			logger.Print("Dropping truncated or invalid data record")
		}
		return d
	}
	for len(p) >= t.FieldLength {
		fr := flowRecord{ValuesMap: make(map[uint16]fields.Value)}
		pos := 0
		for _, f := range t.Fields {
			width := int(f.Length)
			if width == 65535 {
				if len(p)-pos < 1 {
					return malformed()
				}
				width = int(p[pos])
				pos++
				if width == 255 {
					if len(p)-pos < 2 {
						return malformed()
					}
					width = int(binary.BigEndian.Uint16(p[pos:]))
					pos += 2
				}
			}
			if width > len(p)-pos {
				return malformed()
			}
			// IPFIX IE 256 is IANA ethernetType, not Argus's internal timestamp alias (legacy v9 only).
			if function, known := FUNCTIONMAP[f.FieldType]; known && !f.Enterprise && !(n.Header.Version == 10 && f.FieldType == fields.CUSTOM_TIMESTAMP) {
				if !validFieldWidth(f.FieldType, width) && !(width == 0 && variableField(f.FieldType)) {
					return malformed()
				}
				// Values escape to the asynchronous consumer; never retain the reusable UDP buffer.
				valueBytes := append([]byte(nil), p[pos:pos+width]...)
				var value fields.Value
				if integerField(f.FieldType) {
					var data uint64
					for _, b := range valueBytes {
						data = data<<8 | uint64(b)
					}
					value = fields.IntValue{Data: int(data), Bytes: valueBytes}
				} else {
					value = function(valueBytes)
				}
				value.SetType(f.FieldType)
				fr.Values = append(fr.Values, value)
				fr.ValuesMap[f.FieldType] = value
			}
			pos += width
		}
		if pos == 0 {
			return malformed()
		}
		d.Records = append(d.Records, fr)
		p = p[pos:]
	}
	// Padding is shorter than the minimum record and consists of zero octets.
	for _, b := range p {
		if b != 0 {
			return malformed()
		}
	}
	return d
}

/*
ParseTemplate

Slices a flow template out of an overall packet
Requires

	templateSlice []byte : Full packet bytes

Returns

	netFlowPacketTemplate: Struct of template
*/
func parseTemplate(templateSlice []byte) netflowPacketTemplate {
	if len(templateSlice) < 4 {
		return netflowPacketTemplate{}
	}
	t, _, err := readTemplate(templateSlice[4:], 9)
	if err != nil {
		return netflowPacketTemplate{}
	}
	return t
}

/*
Route
Takes an entire packet slice, and routes each flowset to the correct handler

Requires

	nfp netflowPacket : netflowpacket struct
	p []byte        : Packet bytes
	start int           : Byte index to start at (skip the headers, etc)
	logger *log.Logger : Logger instance for warnings
*/
func Route(nfp netflowPacket, p []byte, start int, logger *log.Logger) netflowPacket {
	end := len(p)
	if nfp.Length < end {
		end = nfp.Length
	}
	for start >= 0 && start < end {
		if end-start < 4 {
			if logger != nil {
				logger.Print("Dropping truncated set header")
			}
			break
		}
		id := binary.BigEndian.Uint16(p[start:])
		length := int(binary.BigEndian.Uint16(p[start+2:]))
		if length < 4 || length > end-start {
			if logger != nil {
				logger.Print("Dropping invalid set length")
			}
			break
		}
		s := p[start : start+length]
		switch {
		case (nfp.Header.Version == 10 && id == 2) || (nfp.Header.Version == 9 && id == 0):
			parseTemplates(&nfp, s[4:], logger)
		case (nfp.Header.Version == 10 && id == 3) || (nfp.Header.Version == 9 && id == 1):
			skipOptionsTemplates(&nfp, s[4:], logger)
		case id >= 256:
			nfp.Data = append(nfp.Data, parseData(nfp, s, logger))
		default:
			if logger != nil {
				logger.Printf("Skipping unsupported set ID %d (including options templates)", id)
			}
		}
		start += length
	}
	return nfp
}

func (n *Netflow) Configure(config map[string]string) {
	n.BindAddr = net.ParseIP(config["bindaddr"])

	port, err := strconv.Atoi(config["bindport"])
	if err != nil {
		panic(fmt.Sprintf("Error parsing bindport: %v", err))
	}
	n.BindPort = port

	// Parse debug flag
	n.debug = false // default
	if debugVal, ok := config["debug"]; ok {
		n.debug, err = strconv.ParseBool(debugVal)
		if err != nil {
			panic(fmt.Sprintf("Error parsing debug flag: %v", err))
		}
	}

	// Default log file
	logFileName := "goflow.log"
	var logOutput io.Writer

	if n.debug {
		// Log to file ONLY, not stdout
		file, err := os.OpenFile(logFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			panic(fmt.Sprintf("Error opening log file %s: %v", logFileName, err))
		}
		n.logFile = file
		logOutput = file
	} else {
		// Malformed input must remain visible even when per-flow debug is disabled.
		logOutput = os.Stderr
	}

	n.logger = log.New(logOutput, "[NETFLOW] ", log.Ldate|log.Ltime|log.Lshortfile)

	// Init flow channel
	n.FlowChannel = make(chan map[uint16]fields.Value, 1000)
}

func (nf *Netflow) Start() {
	if nf.logger == nil {
		nf.logger = log.Default()
	}
	if nf.logFile != nil {
		defer func() {
			if err := nf.logFile.Close(); err != nil {
				fmt.Printf("Error closing log file: %v\n", err)
			}
		}()
	}

	if nf.FlowChannel == nil {
		nf.logger.Printf("Warning: FlowChannel is nil. Flows will be dropped.\n")
	}

	addr := net.UDPAddr{
		Port: nf.BindPort,
		IP:   nf.BindAddr,
	}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		nf.logger.Printf("Error listening on UDP: %v\n", err)
		return
	}

	nf.logger.Printf("[NETFLOW] Start() launched on %s:%d", nf.BindAddr, nf.BindPort)
	nf.logger.Printf("Listening for NetFlow on %v\n", addr)

	nf.Templates = make(map[uint32]map[uint32]map[uint16]netflowPacketTemplate)

	defer conn.Close()
	// Large enough for an ordinary full UDP datagram (IPv4 or IPv6).
	packet := make([]byte, 65535)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(packet)
		if err != nil {
			nf.warnings().Printf("Error reading from UDP: %v", err)
			continue
		}
		nf.HandleUDPFlow(remoteAddr, packet[:n])
	}
}

// exporterKey is the template-cache key for an exporter. net.UDPAddr.IP is
// usually the 16-byte IPv4-in-IPv6 form, so reading its first 4 bytes (as this
// used to) gave 0 for every IPv4 exporter: all routers shared one template
// cache, and two exporters using the same template IDs would decode each
// other's flows with the wrong layout.
func exporterKey(ip net.IP) uint32 {
	if v4 := ip.To4(); v4 != nil {
		return binary.BigEndian.Uint32(v4)
	}
	h := fnv.New32a()
	h.Write(ip.To16())
	return h.Sum32()
}

// NEW: Add flowChannel parameter
func NewNetflowCollector(addr string, port int, debug bool, flowChannel chan map[uint16]fields.Value, writer io.Writer) (*Netflow, error) {
	parsedAddr := net.ParseIP(addr)
	if parsedAddr == nil {
		return nil, fmt.Errorf("invalid bind address: %s", addr)
	}

	if writer == nil {
		writer = os.Stderr
	}
	logger := log.New(writer, "GOFLOW: ", log.Ldate|log.Ltime|log.Lshortfile)

	return &Netflow{
		Templates:   make(map[uint32]map[uint32]map[uint16]netflowPacketTemplate),
		BindAddr:    parsedAddr,
		BindPort:    port,
		debug:       debug,
		FlowChannel: flowChannel, // Initialize the channel
		logger:      logger,
	}, nil
}

func (nf *Netflow) HandleUDPFlow(remoteAddr *net.UDPAddr, packet []byte) {
	warnings := nf.warnings()
	if len(packet) < 2 {
		warnings.Print("Dropping short datagram header")
		return
	}
	if nf.Templates == nil {
		nf.Templates = make(map[uint32]map[uint32]map[uint16]netflowPacketTemplate)
	}
	nfpacket := netflowPacket{Source: exporterKey(remoteAddr.IP), Length: len(packet), Templates: nf.Templates}
	p := &nfpacket.Header
	p.Version = binary.BigEndian.Uint16(packet)
	start := 20
	switch p.Version {
	case 9:
		if len(packet) < 20 {
			warnings.Print("Dropping short NetFlow v9 header")
			return
		}
		p.Count = binary.BigEndian.Uint16(packet[2:4])
		p.Uptime = binary.BigEndian.Uint32(packet[4:8])
		p.Usecs = binary.BigEndian.Uint32(packet[8:12])
		p.Sequence = binary.BigEndian.Uint32(packet[12:16])
		p.Id = binary.BigEndian.Uint32(packet[16:20])
	case 10:
		if len(packet) < 16 {
			warnings.Print("Dropping short IPFIX header")
			return
		}
		p.Length = binary.BigEndian.Uint16(packet[2:4])
		if int(p.Length) != len(packet) {
			warnings.Print("Dropping IPFIX message length mismatch")
			return
		}
		p.ExportTime = binary.BigEndian.Uint32(packet[4:8])
		p.Sequence = binary.BigEndian.Uint32(packet[8:12])
		p.Id = binary.BigEndian.Uint32(packet[12:16])
		p.ObservationDomainID = p.Id
		start = 16
	default:
		warnings.Printf("Dropping unsupported NetFlow version %d", p.Version)
		return
	}
	nf.ReportedVersionOnce.Do(func() {
		nf.ReportedVersion = p.Version
		if nf.logger != nil {
			nf.logger.Printf("[NETFLOW] Detected version %d", p.Version)
		}
	})
	nfpacket = Route(nfpacket, packet, start, warnings)
	nf.Templates = nfpacket.Templates

	for _, dfs := range nfpacket.Data {
		for _, record := range dfs.Records {
			if nfpacket.Header.Version == 9 {
				record.calcTime(nfpacket.Header.Uptime, nfpacket.Header.Usecs)
			}
			record.ValuesMap[fields.CUSTOM_EXPORTER_ADDR] = fields.StringValue{Data: remoteAddr.IP.String()}

			if nf.FlowChannel != nil {
				select {
				case nf.FlowChannel <- record.ValuesMap:
				default:
					warnings.Print("Dropping flow: collector channel full")
				}
			}

			if nf.debug {
				var sl []string
				for t, val := range record.ValuesMap {
					sl = append(sl, fmt.Sprintf("(%v)%v", t, val.ToString()))
				}
				nf.logger.Printf("%s", strings.Join(sl, " : "))
			}
		}
	}
}
