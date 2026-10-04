// rj regenerates golden.txt: it parses each case's input with rapidjson as
// radosgw does (Reader::Parse<kParseNumbersAsStringsFlag |
// kParseCommentsFlag> over a StringStream of the input's bytes), through a
// handler that records every event and refuses the one the case names, and
// prints the case followed by the event stream and the result.
//
// Build it against the rapidjson radosgw v19.2.6 and v20.2.4 build with,
// ceph's src/s3select/rapidjson at fcb23c2d:
//
//   g++ -std=c++17 -I <ceph>/src/s3select/rapidjson/include -o rj rj.cc
//   ./rj golden.txt > golden.new && mv golden.new golden.txt
//
// A line of golden.txt is a comment when it starts with '#'. Any other line
// holds three tab-separated fields: the index of the event the handler
// refuses (-1 for none), the input as a Go double-quoted string using only
// \", \\ and \xNN escapes, and the expected events and result, which rj
// rewrites. Events are O and o (object start and end), A and a (array start
// and end), L (a literal), and K, S and N (key, string and raw number)
// followed by their bytes in hex. The result is OK, or rapidjson's message
// and offset; a refusal is "Terminate parsing due to Handler error.".
#include <cstdlib>
#include <iostream>
#include <string>

#include "rapidjson/error/en.h"
#include "rapidjson/reader.h"

using namespace rapidjson;

static std::string hex(const char* s, size_t l) {
  static const char* d = "0123456789abcdef";
  std::string o;
  for (size_t i = 0; i < l; i++) {
    unsigned char c = static_cast<unsigned char>(s[i]);
    o += d[c >> 4];
    o += d[c & 15];
  }
  return o;
}

static int digit(char c) {
  if (c >= '0' && c <= '9') return c - '0';
  if (c >= 'a' && c <= 'f') return c - 'a' + 10;
  return c - 'A' + 10;
}

static std::string unquote(const std::string& q) {
  std::string o;
  for (size_t i = 1; i + 1 < q.size(); i++) {
    if (q[i] != '\\') {
      o += q[i];
    } else if (q[i + 1] == 'x') {
      o += static_cast<char>(digit(q[i + 2]) << 4 | digit(q[i + 3]));
      i += 3;
    } else {
      o += q[++i];
    }
  }
  return o;
}

struct H : public BaseReaderHandler<UTF8<>, H> {
  std::string ev;
  long refuse;
  long n = 0;
  bool add(const std::string& e) {
    ev += e + " ";
    return n++ != refuse;
  }
  bool Null() { return add("L"); }
  bool Bool(bool) { return add("L"); }
  bool StartObject() { return add("O"); }
  bool EndObject(SizeType) { return add("o"); }
  bool StartArray() { return add("A"); }
  bool EndArray(SizeType) { return add("a"); }
  bool Key(const char* s, SizeType l, bool) { return add("K" + hex(s, l)); }
  bool String(const char* s, SizeType l, bool) { return add("S" + hex(s, l)); }
  bool RawNumber(const char* s, SizeType l, bool) { return add("N" + hex(s, l)); }
};

int main(int argc, char** argv) {
  (void)argc;
  std::ios::sync_with_stdio(false);
  std::FILE* f = std::fopen(argv[1], "r");
  std::string line;
  for (int c; (c = std::fgetc(f)) != EOF;) {
    if (c != '\n') {
      line += static_cast<char>(c);
      continue;
    }
    if (line.empty() || line[0] == '#') {
      std::cout << line << "\n";
      line.clear();
      continue;
    }
    size_t t1 = line.find('\t');
    size_t t2 = line.find('\t', t1 + 1);
    std::string refuse = line.substr(0, t1);
    std::string quoted = line.substr(t1 + 1, t2 == std::string::npos ? std::string::npos : t2 - t1 - 1);
    std::string text = unquote(quoted);
    H h;
    h.refuse = std::strtol(refuse.c_str(), nullptr, 10);
    StringStream ss(text.c_str());
    Reader reader;
    ParseResult pr = reader.Parse<kParseNumbersAsStringsFlag | kParseCommentsFlag>(ss, h);
    std::cout << refuse << "\t" << quoted << "\t" << h.ev << "| ";
    if (pr) {
      std::cout << "OK\n";
    } else {
      std::cout << GetParseError_En(pr.Code()) << " @" << pr.Offset() << "\n";
    }
    line.clear();
  }
  std::fclose(f);
  return 0;
}
