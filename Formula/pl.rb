class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.2.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.2.0/pellets_0.2.0_darwin_arm64.tar.gz"
    sha256 "efafed869ac2fd3f2f65754cfe1488fa53691ef9d6013d2ca2e779905b4d5e61"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.2.0/pellets_0.2.0_darwin_amd64.tar.gz"
    sha256 "53dd01b4e73578cd4a5bd20cca964cfde96f175f08c755f1a5503ef2b0787021"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
