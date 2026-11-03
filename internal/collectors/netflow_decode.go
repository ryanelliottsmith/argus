package collectors

import (
	"encoding/binary"
	"fmt"
	"log"
)

// validFieldWidth validates known typed decoders before a definition is cached.
// Unknown elements are opaque: their declared widths still determine alignment.
func validFieldWidth(id uint16, width int) bool {
	if width == 0 {
		return false
	}
	switch id {
	case IPV4_SRC_ADDR, IPV4_DST_ADDR, IPV4_NEXT_HOP, BGP_IPV4_NEXT_HOP,
		IPV4_SRC_PREFIX, IPV4_DST_PREFIX, MPLS_TOP_LABEL_IP_ADDR,
		POST_NAT_SOURCE_IPV4_ADDRESS, POST_NAT_DESTINATION_IPV4_ADDRESS:
		return width == 4
	case IPV6_SRC_ADDR, IPV6_DST_ADDR, IPV6_NEXT_HOP, BPG_IPV6_NEXT_HOP:
		return width == 16
	case IN_SRC_MAC, OUT_DST_MAC, IN_DST_MAC, OUT_SRC_MAC:
		return width == 6
	case IF_NAME, IF_DESC, SAMPLER_NAME, APPLICATION_DESCRIPTION, APPLICATION_NAME,
		MPLS_PAL_RD, DEPRECATED, LAYER2_PACKET_SECTION_DATA:
		return true
	default:
		_, known := FUNCTIONMAP[id]
		return !known || width <= 8
	}
}

func variableField(id uint16) bool {
	switch id {
	case IF_NAME, IF_DESC, SAMPLER_NAME, APPLICATION_DESCRIPTION, APPLICATION_NAME,
		MPLS_PAL_RD, DEPRECATED, LAYER2_PACKET_SECTION_DATA:
		return true
	default:
		_, known := FUNCTIONMAP[id]
		return !known
	}
}

func integerField(id uint16) bool {
	if variableField(id) {
		return false
	}
	// Only integer decoders accept every width from one through eight.
	return validFieldWidth(id, 1) && validFieldWidth(id, 8)
}

func readTemplate(p []byte, version uint16) (netflowPacketTemplate, int, error) {
	var t netflowPacketTemplate
	t.Version = version
	if len(p) < 4 {
		return t, 0, fmt.Errorf("truncated template header")
	}
	t.ID = binary.BigEndian.Uint16(p)
	t.FieldCount = binary.BigEndian.Uint16(p[2:])
	if t.ID < 256 || t.FieldCount == 0 {
		return t, 0, fmt.Errorf("unsupported template ID or empty definition")
	}
	if int(t.FieldCount) > (len(p)-4)/4 {
		return t, 0, fmt.Errorf("truncated template fields")
	}
	pos := 4
	for i := 0; i < int(t.FieldCount); i++ {
		if len(p)-pos < 4 {
			return t, 0, fmt.Errorf("truncated field specifier")
		}
		id := binary.BigEndian.Uint16(p[pos:])
		width := binary.BigEndian.Uint16(p[pos+2:])
		pos += 4
		enterprise := version == 10 && id&0x8000 != 0
		if enterprise {
			if len(p)-pos < 4 {
				return t, 0, fmt.Errorf("truncated enterprise number")
			}
			pos += 4 // Vendor elements stay opaque; do not alias even PEN zero to IANA.
			id &= 0x7fff
		}
		variable := version == 10 && width == 65535
		if width == 0 || (variable && !enterprise && !variableField(id)) || (!variable && !enterprise && !validFieldWidth(id, int(width))) {
			return t, 0, fmt.Errorf("invalid typed field width")
		}
		if variable {
			t.FieldLength++
		} else {
			t.FieldLength += int(width)
		}
		if t.FieldLength > 65535 {
			return t, 0, fmt.Errorf("template record length exceeds message limit")
		}
		t.Fields = append(t.Fields, templateField{FieldType: id, Length: width, Enterprise: enterprise})
	}
	return t, pos, nil
}

func parseTemplates(n *netflowPacket, p []byte, logger *log.Logger) {
	for len(p) > 3 {
		if n.Header.Version == 10 && binary.BigEndian.Uint16(p[2:]) == 0 {
			id := binary.BigEndian.Uint16(p)
			if id < 256 {
				if logger != nil {
					logger.Print("Dropping unsupported all-template withdrawal")
				}
				return
			}
			cached := n.Templates[n.Source][n.Header.Id][id]
			if cached.Version == 10 && !cached.Options {
				delete(n.Templates[n.Source][n.Header.Id], id)
			}
			p = p[4:]
			continue
		}
		t, used, err := readTemplate(p, n.Header.Version)
		if err != nil {
			if logger != nil {
				logger.Printf("Dropping template set: %v", err)
			}
			return
		}
		installTemplate(n, t)
		p = p[used:]
	}
}

func installTemplate(n *netflowPacket, t netflowPacketTemplate) {
	if n.Templates == nil {
		n.Templates = make(map[uint32]map[uint32]map[uint16]netflowPacketTemplate)
	}
	if n.Templates[n.Source] == nil {
		n.Templates[n.Source] = make(map[uint32]map[uint16]netflowPacketTemplate)
	}
	if n.Templates[n.Source][n.Header.Id] == nil {
		n.Templates[n.Source][n.Header.Id] = make(map[uint16]netflowPacketTemplate)
	}
	n.Templates[n.Source][n.Header.Id][t.ID] = t
}

// Only validate enough of an options definition to identify its data-set ID.
// Store a non-flow marker so a previous ordinary template cannot decode it.
func skipOptionsTemplates(n *netflowPacket, p []byte, logger *log.Logger) {
	if logger != nil {
		logger.Print("Skipping unsupported options templates and their data")
	}
	for len(p) >= 4 {
		id := binary.BigEndian.Uint16(p)
		count := int(binary.BigEndian.Uint16(p[2:]))
		if id < 256 {
			return
		} // All-options withdrawal is deliberately unsupported.
		if n.Header.Version == 10 && count == 0 {
			cached := n.Templates[n.Source][n.Header.Id][id]
			if cached.Version == 10 && cached.Options {
				delete(n.Templates[n.Source][n.Header.Id], id)
			}
			p = p[4:]
			continue
		}
		if len(p) < 6 {
			return
		}
		scopes := int(binary.BigEndian.Uint16(p[4:]))
		if n.Header.Version == 9 {
			// v9 encodes scope/option descriptor byte lengths, not counts.
			if count == 0 || count%4 != 0 || scopes%4 != 0 {
				return
			}
			count = (count + scopes) / 4
		} else if scopes == 0 || scopes > count {
			return
		}
		if count > (len(p)-6)/4 {
			return
		}
		pos, total := 6, 0
		for i := 0; i < count; i++ {
			if len(p)-pos < 4 {
				return
			}
			id := binary.BigEndian.Uint16(p[pos:])
			width := int(binary.BigEndian.Uint16(p[pos+2:]))
			if width == 0 {
				return
			}
			pos += 4
			if n.Header.Version == 10 && id&0x8000 != 0 {
				if len(p)-pos < 4 {
					return
				}
				pos += 4
			}
			if n.Header.Version == 10 && width == 65535 {
				width = 1
			}
			total += width
			if total > 65535 {
				return
			}
		}
		installTemplate(n, netflowPacketTemplate{ID: id, Version: n.Header.Version, Options: true})
		p = p[pos:]
	}
}
