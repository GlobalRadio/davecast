// this is a rubish attempt at a first module
package ring

type arbitrary interface{}

type Ring struct {
	size int
	arr []arbitrary
	start int
	items int
}

func New (size int)(*Ring) {
	var r Ring
	r.size = size
	r.start = 0
	r.items = 0
	r.arr = make([]arbitrary, size)
	return &r
}

func (r *Ring) List () {
	for n := 0; n < r.items; n++ {
		println(r.arr[((r.start + n) % r.size)])
	}
}

func (r *Ring) Push (item arbitrary)(bool) {
	if r.items >= r.size {
		// buffer is full - we will lose first entry
		r.items = r.size - 1
		r.start++
	}
	r.arr[((r.start + r.items) % r.size)] = item
	r.items++
	return true
}

func (r *Ring) Shift ()(arbitrary, bool) {
	if r.items == 0 {
		return nil, false
	}

	v := r.arr[r.start % r.size]
	r.start++
	r.items--
	return v, true
}

func (r *Ring) Head ()(arbitrary, bool) {
	if r.items == 0 {
		return nil, false
	}

	v := r.arr[r.start % r.size]
	return v, true
}

func (r *Ring) Peek ()(arbitrary) {
	if r.items == 0 {
		return nil
	}

	v := r.arr[(r.start + r.items - 1) % r.size]
	return v
}


func (r *Ring) Items ()(int) {
	return r.items
}

func (r *Ring) Empty ()(bool) {
	return r.items == 0
}
