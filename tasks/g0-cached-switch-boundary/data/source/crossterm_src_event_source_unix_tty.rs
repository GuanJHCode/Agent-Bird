#[cfg(feature = "libc")]
use std::os::unix::prelude::AsRawFd;
use std::{
    collections::VecDeque,
    io,
    os::unix::net::UnixStream,
    time::{Duration, Instant},
};

#[cfg(not(feature = "libc"))]
use rustix::fd::{AsFd, AsRawFd};

use signal_hook::low_level::pipe;

use crate::event::timeout::PollTimeout;
use crate::event::Event;
use filedescriptor::{poll, pollfd, POLLIN};

#[cfg(feature = "event-stream")]
use crate::event::sys::Waker;
use crate::event::{
    source::EventSource, sys::unix::parse::parse_event, InputDiscardStatus, InternalEvent,
};
use crate::terminal::sys::file_descriptor::{tty_fd, FileDesc};

/// Holds a prototypical Waker and a receiver we can wait on when doing select().
#[cfg(feature = "event-stream")]
struct WakePipe {
    receiver: UnixStream,
    waker: Waker,
}

#[cfg(feature = "event-stream")]
impl WakePipe {
    fn new() -> io::Result<Self> {
        let (receiver, sender) = nonblocking_unix_pair()?;
        Ok(WakePipe {
            receiver,
            waker: Waker::new(sender),
        })
    }
}

// I (@zrzka) wasn't able to read more than 1_022 bytes when testing
// reading on macOS/Linux -> we don't need bigger buffer and 1k of bytes
// is enough.
const TTY_BUFFER_SIZE: usize = 1_024;
const BUFFERED_ESCAPE_TIMEOUT: Duration = Duration::from_millis(20);
const BRACKETED_PASTE_START: &[u8] = b"\x1b[200~";
const BRACKETED_PASTE_END: &[u8] = b"\x1b[201~";

pub(crate) struct UnixInternalEventSource {
    parser: Parser,
    tty_buffer: [u8; TTY_BUFFER_SIZE],
    tty: FileDesc<'static>,
    winch_signal_receiver: UnixStream,
    #[cfg(feature = "event-stream")]
    wake_pipe: WakePipe,
}

fn nonblocking_unix_pair() -> io::Result<(UnixStream, UnixStream)> {
    let (receiver, sender) = UnixStream::pair()?;
    receiver.set_nonblocking(true)?;
    sender.set_nonblocking(true)?;
    Ok((receiver, sender))
}

impl UnixInternalEventSource {
    pub fn new() -> io::Result<Self> {
        UnixInternalEventSource::from_file_descriptor(tty_fd()?)
    }

    pub(crate) fn from_file_descriptor(input_fd: FileDesc<'static>) -> io::Result<Self> {
        Ok(UnixInternalEventSource {
            parser: Parser::default(),
            tty_buffer: [0u8; TTY_BUFFER_SIZE],
            tty: input_fd,
            winch_signal_receiver: {
                let (receiver, sender) = nonblocking_unix_pair()?;
                // Unregistering is unnecessary because EventSource is a singleton
                #[cfg(feature = "libc")]
                pipe::register(libc::SIGWINCH, sender)?;
                #[cfg(not(feature = "libc"))]
                pipe::register(rustix::process::Signal::WINCH.as_raw(), sender)?;
                receiver
            },
            #[cfg(feature = "event-stream")]
            wake_pipe: WakePipe::new()?,
        })
    }
}

/// read_complete reads from a non-blocking file descriptor
/// until the buffer is full or it would block.
///
/// Similar to `std::io::Read::read_to_end`, except this function
/// only fills the given buffer and does not read beyond that.
fn read_complete(fd: &FileDesc, buf: &mut [u8]) -> io::Result<usize> {
    loop {
        match fd.read(buf) {
            Ok(x) => return Ok(x),
            Err(e) => match e.kind() {
                io::ErrorKind::WouldBlock => return Ok(0),
                io::ErrorKind::Interrupted => continue,
                _ => return Err(e),
            },
        }
    }
}

impl EventSource for UnixInternalEventSource {
    fn try_read(&mut self, timeout: Option<Duration>) -> io::Result<Option<InternalEvent>> {
        let timeout = PollTimeout::new(timeout);

        fn make_pollfd<F: AsRawFd>(fd: &F) -> pollfd {
            pollfd {
                fd: fd.as_raw_fd(),
                events: POLLIN,
                revents: 0,
            }
        }

        #[cfg(not(feature = "event-stream"))]
        let mut fds = [
            make_pollfd(&self.tty),
            make_pollfd(&self.winch_signal_receiver),
        ];

        #[cfg(feature = "event-stream")]
        let mut fds = [
            make_pollfd(&self.tty),
            make_pollfd(&self.winch_signal_receiver),
            make_pollfd(&self.wake_pipe.receiver),
        ];

        while timeout.leftover().map_or(true, |t| !t.is_zero())
            || self.parser.pending_escape_expired()
        {
            // check if there are buffered events from the last read
            if let Some(event) = self.parser.next() {
                return Ok(Some(event));
            }
            match poll(&mut fds, self.parser.poll_timeout(timeout.leftover())) {
                Err(filedescriptor::Error::Poll(e)) | Err(filedescriptor::Error::Io(e)) => {
                    match e.kind() {
                        // retry on EINTR
                        io::ErrorKind::Interrupted => continue,
                        _ => return Err(e),
                    }
                }
                Err(e) => {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::Other,
                        format!("got unexpected error while polling: {:?}", e),
                    ))
                }
                Ok(_) => (),
            };
            if fds[0].revents & POLLIN != 0 {
                loop {
                    let read_count = read_complete(&self.tty, &mut self.tty_buffer)?;
                    if read_count > 0 {
                        self.parser.advance(
                            &self.tty_buffer[..read_count],
                            read_count == TTY_BUFFER_SIZE,
                        );
                    }

                    if let Some(event) = self.parser.next() {
                        return Ok(Some(event));
                    }

                    // The source owns this descriptor for the lifetime of its borrow.
                    let fd = unsafe { rustix::fd::BorrowedFd::borrow_raw(self.tty.raw_fd()) };
                    if read_count == 0 || rustix::io::ioctl_fionread(fd)? == 0 {
                        break;
                    }
                    if timeout.elapsed() {
                        return Ok(self.parser.finish_pending_escape());
                    }
                }
            }
            if let Some(event) = self.parser.finish_pending_escape() {
                return Ok(Some(event));
            }
            if fds[1].revents & POLLIN != 0 {
                #[cfg(feature = "libc")]
                let fd = FileDesc::new(self.winch_signal_receiver.as_raw_fd(), false);
                #[cfg(not(feature = "libc"))]
                let fd = FileDesc::Borrowed(self.winch_signal_receiver.as_fd());
                // drain the pipe
                while read_complete(&fd, &mut [0; 1024])? != 0 {}
                // TODO Should we remove tput?
                //
                // This can take a really long time, because terminal::size can
                // launch new process (tput) and then it parses its output. It's
                // not a really long time from the absolute time point of view, but
                // it's a really long time from the mio, async-std/tokio executor, ...
                // point of view.
                let new_size = crate::terminal::size()?;
                return Ok(Some(InternalEvent::Event(Event::Resize(
                    new_size.0, new_size.1,
                ))));
            }

            #[cfg(feature = "event-stream")]
            if fds[2].revents & POLLIN != 0 {
                #[cfg(feature = "libc")]
                let fd = FileDesc::new(self.wake_pipe.receiver.as_raw_fd(), false);
                #[cfg(not(feature = "libc"))]
                let fd = FileDesc::Borrowed(self.wake_pipe.receiver.as_fd());
                // drain the pipe
                while read_complete(&fd, &mut [0; 1024])? != 0 {}

                return Err(std::io::Error::new(
                    std::io::ErrorKind::Interrupted,
                    "Poll operation was woken up by `Waker::wake`",
                ));
            }
        }
        Ok(self.parser.finish_pending_escape())
    }

    fn buffer_input(&mut self, input: &[u8], events: &mut VecDeque<InternalEvent>) {
        self.parser.buffer_external_input(input);
        events.extend(
            self.parser
                .by_ref()
                .filter(|event| matches!(event, InternalEvent::Event(_))),
        );
    }

    fn discard_buffered_input(&mut self) -> InputDiscardStatus {
        self.parser.discard_buffered_input()
    }

    #[cfg(feature = "event-stream")]
    fn waker(&self) -> Waker {
        self.wake_pipe.waker.clone()
    }
}

//
// Following `Parser` structure exists for two reasons:
//
//  * mimic anes Parser interface
//  * move the advancing, parsing, ... stuff out of the `try_read` method
//
#[derive(Debug)]
struct Parser {
    buffer: Vec<u8>,
    internal_events: VecDeque<InternalEvent>,
    pending_escape_deadline: Option<Instant>,
    discarded_sequence: Option<DiscardedSequence>,
}

#[derive(Debug, Clone, Copy)]
enum DiscardedSequence {
    PasteStart(usize),
    PasteBody(usize),
    OscBody,
    OscEscape,
    Csi,
    X10Mouse(usize),
    Ss3,
}

impl Default for Parser {
    fn default() -> Self {
        Parser {
            // This buffer is used for -> 1 <- ANSI escape sequence. Are we
            // aware of any ANSI escape sequence that is bigger? Can we make
            // it smaller?
            //
            // Probably not worth spending more time on this as "there's a plan"
            // to use the anes crate parser.
            buffer: Vec::with_capacity(256),
            // TTY_BUFFER_SIZE is 1_024 bytes. How many ANSI escape sequences can
            // fit? What is an average sequence length? Let's guess here
            // and say that the average ANSI escape sequence length is 8 bytes. Thus
            // the buffer size should be 1024/8=128 to avoid additional allocations
            // when processing large amounts of data.
            //
            // There's no need to make it bigger, because when you look at the `try_read`
            // method implementation, all events are consumed before the next TTY_BUFFER
            // is processed -> events pushed.
            internal_events: VecDeque::with_capacity(128),
            pending_escape_deadline: None,
            discarded_sequence: None,
        }
    }
}

impl Parser {
    fn discard_buffered_input(&mut self) -> InputDiscardStatus {
        if self.discarded_sequence.is_none() {
            if self.buffer.starts_with(BRACKETED_PASTE_START) {
                let matched = (1..BRACKETED_PASTE_END.len())
                    .rev()
                    .find(|&matched| self.buffer.ends_with(&BRACKETED_PASTE_END[..matched]))
                    .unwrap_or(0);
                self.discarded_sequence = Some(DiscardedSequence::PasteBody(matched));
            } else if !self.buffer.is_empty() && BRACKETED_PASTE_START.starts_with(&self.buffer) {
                self.discarded_sequence = Some(DiscardedSequence::PasteStart(self.buffer.len()));
            } else if self.buffer.starts_with(b"\x1b]") {
                self.discarded_sequence = Some(if self.buffer.ends_with(b"\x1b") {
                    DiscardedSequence::OscEscape
                } else {
                    DiscardedSequence::OscBody
                });
            } else if self.buffer.starts_with(b"\x1b[M") {
                self.discarded_sequence = Some(DiscardedSequence::X10Mouse(6 - self.buffer.len()));
            } else if self.buffer.starts_with(b"\x1b[") {
                self.discarded_sequence = Some(DiscardedSequence::Csi);
            } else if self.buffer.starts_with(b"\x1bO") {
                self.discarded_sequence = Some(DiscardedSequence::Ss3);
            }
        }
        self.buffer.clear();
        self.internal_events.clear();
        self.pending_escape_deadline = None;
        match self.discarded_sequence {
            Some(DiscardedSequence::PasteStart(_) | DiscardedSequence::PasteBody(_)) => {
                InputDiscardStatus::BracketedPasteInProgress
            }
            Some(_) => InputDiscardStatus::ControlSequenceInProgress,
            None => InputDiscardStatus::Complete,
        }
    }

    fn buffer_external_input(&mut self, buffer: &[u8]) {
        self.advance(buffer, true);
        if self.buffer.as_slice() == b"\x1b" {
            self.pending_escape_deadline = Some(Instant::now() + BUFFERED_ESCAPE_TIMEOUT);
        }
    }

    fn poll_timeout(&self, timeout: Option<Duration>) -> Option<Duration> {
        let Some(deadline) = self.pending_escape_deadline else {
            return timeout;
        };
        let remaining = deadline.saturating_duration_since(Instant::now());
        Some(timeout.map_or(remaining, |timeout| timeout.min(remaining)))
    }

    fn pending_escape_expired(&self) -> bool {
        self.pending_escape_deadline
            .map_or(false, |deadline| Instant::now() >= deadline)
    }

    fn finish_pending_escape(&mut self) -> Option<InternalEvent> {
        let deadline = self.pending_escape_deadline?;
        if Instant::now() < deadline {
            return None;
        }
        self.pending_escape_deadline = None;
        let event = parse_event(&self.buffer, false).ok().flatten()?;
        self.buffer.clear();
        Some(event)
    }

    fn advance(&mut self, buffer: &[u8], more: bool) {
        self.pending_escape_deadline = None;
        for (idx, byte) in buffer.iter().enumerate() {
            if let Some(discarded_sequence) = self.discarded_sequence {
                self.discarded_sequence = match discarded_sequence {
                    DiscardedSequence::PasteStart(matched)
                        if *byte == BRACKETED_PASTE_START[matched] =>
                    {
                        if matched + 1 == BRACKETED_PASTE_START.len() {
                            Some(DiscardedSequence::PasteBody(0))
                        } else {
                            Some(DiscardedSequence::PasteStart(matched + 1))
                        }
                    }
                    DiscardedSequence::PasteStart(_) if *byte == BRACKETED_PASTE_START[0] => {
                        Some(DiscardedSequence::PasteStart(1))
                    }
                    DiscardedSequence::PasteStart(1) if *byte == b']' => {
                        Some(DiscardedSequence::OscBody)
                    }
                    DiscardedSequence::PasteStart(1) if *byte == b'O' => {
                        Some(DiscardedSequence::Ss3)
                    }
                    DiscardedSequence::PasteStart(2) if *byte == b'M' => {
                        Some(DiscardedSequence::X10Mouse(3))
                    }
                    DiscardedSequence::PasteStart(2) if *byte == b'[' => {
                        Some(DiscardedSequence::Csi)
                    }
                    DiscardedSequence::PasteStart(matched)
                        if matched >= 2 && !(0x40..=0x7e).contains(byte) =>
                    {
                        Some(DiscardedSequence::Csi)
                    }
                    DiscardedSequence::PasteStart(_) => None,
                    DiscardedSequence::PasteBody(matched)
                        if *byte == BRACKETED_PASTE_END[matched] =>
                    {
                        if matched + 1 == BRACKETED_PASTE_END.len() {
                            None
                        } else {
                            Some(DiscardedSequence::PasteBody(matched + 1))
                        }
                    }
                    DiscardedSequence::PasteBody(_) if *byte == BRACKETED_PASTE_END[0] => {
                        Some(DiscardedSequence::PasteBody(1))
                    }
                    DiscardedSequence::PasteBody(_) => Some(DiscardedSequence::PasteBody(0)),
                    DiscardedSequence::OscBody | DiscardedSequence::OscEscape
                        if *byte == b'\x07' =>
                    {
                        None
                    }
                    DiscardedSequence::OscEscape if *byte == b'\\' => None,
                    DiscardedSequence::OscBody | DiscardedSequence::OscEscape
                        if *byte == b'\x1b' =>
                    {
                        Some(DiscardedSequence::OscEscape)
                    }
                    DiscardedSequence::OscBody | DiscardedSequence::OscEscape => {
                        Some(DiscardedSequence::OscBody)
                    }
                    DiscardedSequence::Csi | DiscardedSequence::Ss3 if *byte == b'\x1b' => {
                        Some(DiscardedSequence::PasteStart(1))
                    }
                    DiscardedSequence::Csi | DiscardedSequence::Ss3
                        if (0x40..=0x7e).contains(byte) =>
                    {
                        None
                    }
                    DiscardedSequence::Csi => Some(DiscardedSequence::Csi),
                    DiscardedSequence::X10Mouse(remaining) if remaining > 1 => {
                        Some(DiscardedSequence::X10Mouse(remaining - 1))
                    }
                    DiscardedSequence::X10Mouse(_) => None,
                    DiscardedSequence::Ss3 => Some(DiscardedSequence::Ss3),
                };
                continue;
            }
            let more = idx + 1 < buffer.len() || more;

            self.buffer.push(*byte);

            match parse_event(&self.buffer, more) {
                Ok(Some(ie)) => {
                    self.internal_events.push_back(ie);
                    self.buffer.clear();
                }
                Ok(None) => {
                    let completed_osc = self.buffer.starts_with(b"\x1b]")
                        && (*byte == b'\x07'
                            || (*byte == b'\\'
                                && self.buffer.get(self.buffer.len().saturating_sub(2))
                                    == Some(&b'\x1b')));
                    let completed_csi = self.buffer.len() > 2
                        && self.buffer.starts_with(b"\x1b[")
                        && (0x40..=0x7e).contains(byte)
                        && !self.buffer.starts_with(BRACKETED_PASTE_START)
                        && !self.buffer.starts_with(b"\x1b[[")
                        && !self.buffer.starts_with(b"\x1b[M");
                    if completed_osc || completed_csi {
                        self.buffer.clear();
                    }
                }
                Err(_) => {
                    // Event can't be parsed (not enough parameters, parameter is not a number, ...).
                    // Clear the buffer and continue with another sequence.
                    self.buffer.clear();
                }
            }
        }
    }
}

impl Iterator for Parser {
    type Item = InternalEvent;

    fn next(&mut self) -> Option<Self::Item> {
        self.internal_events.pop_front()
    }
}

#[cfg(test)]
mod tests {
    use super::{Duration, Instant, Parser, UnixInternalEventSource};
    use crate::event::{source::EventSource, Event, InputDiscardStatus, InternalEvent, KeyCode};
    use crate::terminal::sys::file_descriptor::FileDesc;
    use std::{io::Write, os::unix::net::UnixStream};

    fn source_with_input() -> (UnixInternalEventSource, UnixStream) {
        let (reader, writer) = UnixStream::pair().unwrap();
        #[cfg(feature = "libc")]
        let reader = {
            use std::os::fd::IntoRawFd;
            FileDesc::new(reader.into_raw_fd(), true)
        };
        #[cfg(not(feature = "libc"))]
        let reader = FileDesc::Owned(reader.into());
        (
            UnixInternalEventSource::from_file_descriptor(reader).unwrap(),
            writer,
        )
    }

    #[test]
    fn externally_buffered_escape_remains_available_for_its_continuation() {
        let mut parser = Parser::default();
        parser.buffer_external_input(b"\x1b");
        assert_eq!(parser.next(), None);

        parser.advance(b"[A", false);
        assert_eq!(
            parser.next(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Up.into())))
        );
    }

    #[test]
    fn standalone_buffered_escape_is_emitted_after_its_ambiguity_window() {
        let mut parser = Parser::default();
        parser.buffer_external_input(b"\x1b");
        assert_eq!(parser.finish_pending_escape(), None);

        parser.pending_escape_deadline = Some(Instant::now());
        assert_eq!(
            parser.finish_pending_escape(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Esc.into())))
        );
    }

    #[test]
    fn discarded_escape_suppresses_a_delayed_modifier_suffix() {
        for suffix in [b'y', b'1'] {
            let mut parser = Parser::default();
            parser.buffer_external_input(b"\x1b");
            parser.pending_escape_deadline = Some(Instant::now());

            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::BracketedPasteInProgress
            );
            assert_eq!(parser.finish_pending_escape(), None);
            parser.advance(std::slice::from_ref(&suffix), false);

            assert_eq!(parser.next(), None);
            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::Complete
            );
        }
    }

    #[test]
    fn discarded_bracketed_paste_suppresses_delayed_input_until_its_end() {
        let mut parser = Parser::default();
        parser.advance(b"\x1b[200~unfinished", true);
        assert_eq!(parser.next(), None);

        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );
        parser.advance(b"1y\r\x1b[20", true);
        assert_eq!(parser.next(), None);
        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );
        parser.advance(b"1~n", false);
        assert_eq!(
            parser.next(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
        );
        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::Complete
        );
    }

    #[test]
    fn discarded_partial_paste_marker_preserves_its_remaining_boundary() {
        let mut parser = Parser::default();
        parser.advance(b"\x1b[2", true);

        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );
        parser.advance(b"00~1y\r\x1b[201~n", false);

        assert_eq!(
            parser.next(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
        );
    }

    #[test]
    fn discarded_partial_mouse_sequence_preserves_all_remaining_payload_bytes() {
        for sequence in [b"\x1b[M @y".as_slice(), b"\x1b[M\x1b@y".as_slice()] {
            for boundary in 1..sequence.len() {
                let mut parser = Parser::default();
                parser.advance(&sequence[..boundary], true);
                assert_ne!(
                    parser.discard_buffered_input(),
                    InputDiscardStatus::Complete
                );

                let remainder = &sequence[boundary..];
                for (index, byte) in remainder.iter().enumerate() {
                    parser.advance(std::slice::from_ref(byte), false);
                    assert_eq!(parser.next(), None);
                    assert_eq!(
                        parser.discard_buffered_input() == InputDiscardStatus::Complete,
                        index + 1 == remainder.len(),
                    );
                }

                parser.advance(b"n", false);
                assert_eq!(
                    parser.next(),
                    Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
                );
            }
        }
    }

    #[test]
    fn completed_unsupported_control_sequences_do_not_remain_buffered() {
        for sequence in [
            b"\x1b]52;c;ignored\x07".as_slice(),
            b"\x1b]52;c;ignored\x1b\\".as_slice(),
            b"\x1b[?1h".as_slice(),
            b"\x1b[?1;2$y".as_slice(),
        ] {
            let mut parser = Parser::default();
            for byte in sequence {
                parser.advance(std::slice::from_ref(byte), true);
            }
            assert!(parser.buffer.is_empty());
            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::Complete
            );
            parser.advance(b"n", false);

            assert_eq!(
                parser.next(),
                Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
            );
        }
    }

    #[test]
    fn completed_control_sequence_preserves_a_following_incomplete_boundary() {
        for (incomplete, continuation, status) in [
            (
                b"\x1b[2".as_slice(),
                b"00~1y\r\x1b[201~n".as_slice(),
                InputDiscardStatus::BracketedPasteInProgress,
            ),
            (
                b"\x1b]10;unfinished".as_slice(),
                b"1y\r\x07n".as_slice(),
                InputDiscardStatus::ControlSequenceInProgress,
            ),
        ] {
            let mut parser = Parser::default();
            let mut input = b"\x1b]52;c;ignored\x07".to_vec();
            input.extend_from_slice(incomplete);
            parser.advance(&input, true);
            assert_eq!(parser.discard_buffered_input(), status);
            parser.advance(continuation, false);

            assert_eq!(
                parser.next(),
                Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
            );
        }
    }

    #[test]
    fn legacy_mouse_and_function_key_sequences_remain_recognized() {
        let mut parser = Parser::default();
        parser.advance(b"\x1b[[A", false);
        assert_eq!(
            parser.next(),
            Some(InternalEvent::Event(Event::Key(KeyCode::F(1).into())))
        );

        parser.advance(b"\x1b[M !!", false);
        assert!(matches!(
            parser.next(),
            Some(InternalEvent::Event(Event::Mouse(_)))
        ));
    }

    #[test]
    fn discarded_double_bracket_csi_suppresses_its_delayed_suffix() {
        for (suffix, completion) in [
            (b"[A".as_slice(), b"".as_slice()),
            (b"[B".as_slice(), b"".as_slice()),
            (b"[C".as_slice(), b"".as_slice()),
            (b"[D".as_slice(), b"".as_slice()),
            (b"[E".as_slice(), b"".as_slice()),
            (b"[y".as_slice(), b"".as_slice()),
            (b"[1".as_slice(), b"A".as_slice()),
            (b"[1y".as_slice(), b"".as_slice()),
        ] {
            let mut sequence = b"\x1b[".to_vec();
            sequence.extend_from_slice(suffix);
            for boundary in 1..sequence.len().min(4) {
                let mut parser = Parser::default();
                parser.advance(&sequence[..boundary], true);
                assert_ne!(
                    parser.discard_buffered_input(),
                    InputDiscardStatus::Complete
                );

                parser.advance(&sequence[boundary..], true);
                assert_eq!(parser.next(), None);
                if !completion.is_empty() {
                    assert_eq!(
                        parser.discard_buffered_input(),
                        InputDiscardStatus::ControlSequenceInProgress
                    );
                    parser.advance(completion, true);
                }
                assert_eq!(
                    parser.discard_buffered_input(),
                    InputDiscardStatus::Complete
                );

                parser.advance(b"n", false);
                assert_eq!(
                    parser.next(),
                    Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
                );
            }
        }
    }

    #[test]
    fn discarded_osc_suppresses_delayed_input_until_its_terminator() {
        for terminator in [b"\x07".as_slice(), b"\x1b\\".as_slice()] {
            let mut parser = Parser::default();
            parser.advance(b"\x1b]10;unfinished", true);
            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::ControlSequenceInProgress
            );

            parser.advance(b"1y\r", true);
            assert!(parser.buffer.is_empty());
            assert_eq!(parser.next(), None);
            for byte in terminator {
                parser.advance(std::slice::from_ref(byte), true);
            }
            parser.advance(b"n", false);

            assert_eq!(
                parser.next(),
                Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
            );
            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::Complete
            );
        }
    }

    #[test]
    fn discarded_control_sequences_suppress_delayed_input_until_the_final_byte() {
        for prefix in [b"\x1b[?".as_slice(), b"\x1bO".as_slice()] {
            let mut parser = Parser::default();
            parser.advance(prefix, true);
            assert_eq!(
                parser.discard_buffered_input(),
                InputDiscardStatus::ControlSequenceInProgress
            );

            parser.advance(b"1\r", true);
            assert!(parser.buffer.is_empty());
            assert_eq!(parser.next(), None);
            parser.advance(b"An", false);

            assert_eq!(
                parser.next(),
                Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
            );
        }
    }

    #[test]
    fn ambiguous_paste_prefix_remains_quarantined_as_a_control_sequence() {
        let mut parser = Parser::default();
        parser.advance(b"\x1b[2", true);
        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );

        parser.advance(b"1\r", true);
        assert_eq!(parser.next(), None);
        assert_eq!(
            parser.discard_buffered_input(),
            InputDiscardStatus::ControlSequenceInProgress
        );
        parser.advance(b"~n", false);

        assert_eq!(
            parser.next(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
        );
    }

    #[test]
    fn discarded_sequences_preserve_a_buffered_terminator_prefix() {
        for (prefix, suffix, status) in [
            (
                b"\x1b]10;unfinished\x1b".as_slice(),
                b"\\n".as_slice(),
                InputDiscardStatus::ControlSequenceInProgress,
            ),
            (
                b"\x1b[200~unfinished\x1b[20".as_slice(),
                b"1~n".as_slice(),
                InputDiscardStatus::BracketedPasteInProgress,
            ),
        ] {
            let mut parser = Parser::default();
            parser.advance(prefix, true);
            assert_eq!(parser.discard_buffered_input(), status);
            parser.advance(suffix, false);

            assert_eq!(
                parser.next(),
                Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
            );
        }
    }

    #[test]
    fn discarded_paste_uses_constant_buffer_space() {
        let mut parser = Parser::default();
        parser.advance(b"\x1b[200~", true);
        parser.discard_buffered_input();

        parser.advance(&[b'y'; 1_024], true);

        assert!(parser.buffer.is_empty());
        assert_eq!(parser.next(), None);
    }

    #[test]
    fn expired_buffered_escape_reads_already_available_continuation_first() {
        let (mut source, mut writer) = source_with_input();
        source.parser.buffer_external_input(b"\x1b");
        source.parser.pending_escape_deadline = Some(Instant::now());
        writer.write_all(b"[A").unwrap();

        assert_eq!(
            source.try_read(Some(Duration::ZERO)).unwrap(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Up.into())))
        );
    }

    #[test]
    fn expired_buffered_escape_remains_usable_without_a_continuation() {
        let (mut source, _writer) = source_with_input();
        source.parser.buffer_external_input(b"\x1b");
        source.parser.pending_escape_deadline = Some(Instant::now());

        assert_eq!(
            source.try_read(Some(Duration::ZERO)).unwrap(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Esc.into())))
        );
        assert_eq!(
            source.discard_buffered_input(),
            InputDiscardStatus::Complete
        );
    }

    #[test]
    fn discarded_paste_respects_poll_timeout_on_a_blocking_descriptor() {
        let (mut source, mut writer) = source_with_input();
        source.parser.advance(b"\x1b[200~", true);
        assert_eq!(
            source.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );

        writer.write_all(b"1y\r").unwrap();
        assert_eq!(
            source.try_read(Some(Duration::from_millis(25))).unwrap(),
            None
        );
        assert_eq!(
            source.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );

        writer.write_all(b"\x1b[201~").unwrap();
        assert_eq!(
            source.try_read(Some(Duration::from_millis(25))).unwrap(),
            None
        );
        assert_eq!(
            source.discard_buffered_input(),
            InputDiscardStatus::Complete
        );
    }

    #[test]
    fn discarded_paste_drains_input_larger_than_the_read_buffer() {
        let (mut source, mut writer) = source_with_input();
        source.parser.advance(b"\x1b[200~", true);
        source.discard_buffered_input();
        let mut input = vec![b'y'; super::TTY_BUFFER_SIZE * 2 + 1];
        input.extend_from_slice(b"\x1b[201~n");
        writer.write_all(&input).unwrap();

        assert_eq!(
            source.try_read(Some(Duration::from_millis(100))).unwrap(),
            Some(InternalEvent::Event(Event::Key(KeyCode::Char('n').into())))
        );
    }

    #[test]
    fn continuously_readable_discarded_paste_respects_poll_deadline() {
        let (mut source, mut writer) = source_with_input();
        source.parser.advance(b"\x1b[200~", true);
        assert_eq!(
            source.discard_buffered_input(),
            InputDiscardStatus::BracketedPasteInProgress
        );

        let (ready_tx, ready_rx) = std::sync::mpsc::sync_channel(1);
        let producer = std::thread::spawn(move || {
            let chunk = [b'y'; super::TTY_BUFFER_SIZE];
            for index in 0..65_536 {
                if writer.write_all(&chunk).is_err() {
                    break;
                }
                if index == 0 {
                    ready_tx.send(()).unwrap();
                }
            }
        });
        ready_rx.recv().unwrap();

        for _ in 0..2 {
            assert_eq!(
                source.try_read(Some(Duration::from_millis(10))).unwrap(),
                None
            );
            assert_eq!(
                source.discard_buffered_input(),
                InputDiscardStatus::BracketedPasteInProgress
            );
            let fd = unsafe { rustix::fd::BorrowedFd::borrow_raw(source.tty.raw_fd()) };
            assert!(rustix::io::ioctl_fionread(fd).unwrap() > 0);
        }

        drop(source);
        producer.join().unwrap();
    }
}
