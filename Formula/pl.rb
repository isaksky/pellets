class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.4.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.4.0/pellets_0.4.0_darwin_arm64.tar.gz"
    sha256 "617c40698ccceb0e63570b36b847f107ad18beade29900e448af7c029593b37b"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.4.0/pellets_0.4.0_darwin_amd64.tar.gz"
    sha256 "5d86185edd6b40a5c79230c440b249b92f612668641011b68befa4456b3693b2"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
