class Pl < Formula
  desc "Local SQLite task queue for coding agents"
  homepage "https://github.com/isaksky/pellets"
  version "0.1.0"
  license "Apache-2.0"

  depends_on :macos

  if Hardware::CPU.arm?
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_arm64.tar.gz"
    sha256 "377b13e48ced38a7ffe7448cfba683e6fecf595d0911faeb59ccb56605ad42f7"
  else
    url "https://github.com/isaksky/pellets/releases/download/v0.1.0/pellets_0.1.0_darwin_amd64.tar.gz"
    sha256 "2a962bd23d73e39f4531a7771ae3e16aace786d8bbf74f3968217cacc190985b"
  end

  def install
    bin.install "pl"
  end

  test do
    assert_equal "pl #{version} (JSON schema 1)", shell_output("#{bin}/pl --version").strip
  end
end
