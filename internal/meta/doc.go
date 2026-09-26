// Package meta holds RGW's stored metadata types with encoders and decoders
// that are byte-compatible with radosgw's. Each type mirrors one C++ type and
// transcribes its encode and decode bodies: decoders accept every struct
// version the C++ decoder accepts, and encoders write the version the target
// release writes. JSON forms follow the encode_json RGW uses for the type as
// a field of a larger struct. Where a type's dump differs between releases,
// JSON follows v20.2.4's; a field only main has appears in JSON only when it
// is set.
package meta
