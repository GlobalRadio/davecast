#!/usr/bin/perl
use strict;
use POSIX ":sys_wait_h";

# SHOULD BE PATCHED TO NOT REQUIRE CURL BINARY

# simple script to mirror an icecast server into davecast
# usage: ./daveice.pl <source-binary> <icecast-server> <relay-server> ...
# eg.:   ./daveice.pl ./daveice 192.168.1.1:80 192.168.2.2:8001 192.168.3.3:8001

my $binary = shift;
my $source = shift;
my @relay = @ARGV;

my %streams;
my %children;

sub streams {
    my($host, $port) = @_;
    my @streams;
    my $str = 'curl -s http://%s/|grep "<a href=\"/"|sed -e "s:.*\"/::" -e "s/\.xspf.*//"';
    my $cmd = sprintf($str, $source);
    open(CURL, '-|', "$cmd") or return ();
    while(<CURL>) {
	chop;
	push @streams, $_;
    }
    close(CURL);
    return @streams;
}


while(1) {
    my @streams = streams($source);
    
    foreach my $stream (@streams) {
	if(! defined $streams{$stream}) {
	    print "$stream\n";

	    if(my $pid = fork()) {
		# parent
		$streams{$stream} = $pid;
		$children{$pid} = $stream;
	    } elsif(defined $pid) {
		# child
		exec($binary, $source, $stream, @relay);
		die;
	    } else {
		# err
		die;
	    }
	}
    }

    my $kid;
    
    do {
	$kid = waitpid(-1, WNOHANG);
	if($kid > 0) {
	    my $stream = $children{$kid};
	    delete $children{$kid};
	    delete $streams{$stream};
	    print "$kid ($stream) died\n";
	}
    } while $kid > 0;
    
	
    sleep(60);
}

