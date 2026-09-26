package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// Realm is RGWRealm, the realms.<id> object.
type Realm struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CurrentPeriod string `json:"current_period"`
	// Epoch is incremented for each new period.
	Epoch uint32 `json:"epoch"`
}

// Encode mirrors RGWRealm::encode, ENCODE_START(1, 1), with the id and name
// in the RGWSystemMetaObj framing.
func (r Realm) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	encodeSysObj(e, r.ID, r.Name)
	e.String(r.CurrentPeriod)
	e.U32(r.Epoch)
	e.EndStruct(f)
}

// DecodeRealm mirrors RGWRealm::decode.
func DecodeRealm(d *denc.Decoder) Realm {
	h := d.BeginStruct(1)
	var r Realm
	r.ID, r.Name = decodeSysObj(d)
	r.CurrentPeriod = d.String()
	r.Epoch = d.U32()
	d.EndStruct(h)
	return r
}
