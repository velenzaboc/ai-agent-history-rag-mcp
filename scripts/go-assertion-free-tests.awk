# Reports Go test functions that never once reference their own *testing.T
# parameter. Such a function has no way to fail and is not a test.
#
# The detector is deliberately narrow. A test that delegates its checking to a
# helper still passes t to that helper, so it references the identifier and is
# not reported. Widening this to "no assert/require/check call" would
# false-positive on table-driven tests, which is why it is not done.
#
# Relies on gofmt: a top-level function's closing brace is a bare "}" in the
# first column.

function flush() {
  if (inTest) {
    if (!seen) {
      printf "ASSERTION-FREE TEST  %s:%d: %s never references its %s parameter\n", FILENAME, startline, name, ident
    }
    inTest = 0
  }
}

/^func Test[A-Za-z0-9_]*\([A-Za-z_][A-Za-z0-9_]* \*testing\.T\)[ \t]*{[ \t]*$/ {
  flush()
  inTest = 1
  seen = 0
  startline = FNR
  name = $2
  sub(/\(.*/, "", name)
  ident = $0
  sub(/^[^(]*\(/, "", ident)
  sub(/ \*testing\.T\).*/, "", ident)
  next
}

/^}[ \t]*$/ { flush(); next }

inTest {
  if ($0 ~ ("(^|[^A-Za-z0-9_])" ident "([^A-Za-z0-9_]|$)")) seen = 1
}

END { flush() }
