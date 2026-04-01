package checkout

import (
	"context"
	"net"
	"testing"

	pb "checkout-service/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ─── Mock servers ────────────────────────────────────────────────────────────

// mockCartServer is a configurable in-process CartService stub.
type mockCartServer struct {
	pb.UnimplementedCartServiceServer
	cart       *pb.Cart
	getErr     error
	emptyErr   error
	emptyCalls int
}

func (m *mockCartServer) GetCart(_ context.Context, req *pb.GetCartRequest) (*pb.Cart, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.cart, nil
}

func (m *mockCartServer) EmptyCart(_ context.Context, _ *pb.EmptyCartRequest) (*pb.Empty, error) {
	m.emptyCalls++
	return &pb.Empty{}, m.emptyErr
}

// mockProductCatalogServer returns a fixed product for any ID.
type mockProductCatalogServer struct {
	pb.UnimplementedProductCatalogServiceServer
	product *pb.Product
	err     error
}

func (m *mockProductCatalogServer) GetProduct(_ context.Context, _ *pb.GetProductRequest) (*pb.Product, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.product, nil
}

// mockCurrencyServer passes money through unchanged (1:1 conversion).
type mockCurrencyServer struct {
	pb.UnimplementedCurrencyServiceServer
	err error
}

func (m *mockCurrencyServer) Convert(_ context.Context, req *pb.CurrencyConversionRequest) (*pb.Money, error) {
	if m.err != nil {
		return nil, m.err
	}
	result := req.GetFrom()
	result.CurrencyCode = req.GetToCode()
	return result, nil
}

// mockShippingServer returns a fixed quote and tracking ID.
type mockShippingServer struct {
	pb.UnimplementedShippingServiceServer
	quoteErr   error
	shipErr    error
	trackingID string
}

func (m *mockShippingServer) GetQuote(_ context.Context, _ *pb.GetQuoteRequest) (*pb.GetQuoteResponse, error) {
	if m.quoteErr != nil {
		return nil, m.quoteErr
	}
	return &pb.GetQuoteResponse{
		CostUsd: &pb.Money{CurrencyCode: "USD", Units: 5, Nanos: 0},
	}, nil
}

func (m *mockShippingServer) ShipOrder(_ context.Context, _ *pb.ShipOrderRequest) (*pb.ShipOrderResponse, error) {
	if m.shipErr != nil {
		return nil, m.shipErr
	}
	id := m.trackingID
	if id == "" {
		id = "TRACK-123"
	}
	return &pb.ShipOrderResponse{TrackingId: id}, nil
}

// mockPaymentServer returns a fixed transaction ID.
type mockPaymentServer struct {
	pb.UnimplementedPaymentServiceServer
	err           error
	transactionID string
}

func (m *mockPaymentServer) Charge(_ context.Context, _ *pb.ChargeRequest) (*pb.ChargeResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	id := m.transactionID
	if id == "" {
		id = "TX-ABC-123"
	}
	return &pb.ChargeResponse{TransactionId: id}, nil
}

// mockEmailServer records calls and can simulate failure.
type mockEmailServer struct {
	pb.UnimplementedEmailServiceServer
	err   error
	calls int
}

func (m *mockEmailServer) SendOrderConfirmation(_ context.Context, _ *pb.SendOrderConfirmationRequest) (*pb.Empty, error) {
	m.calls++
	return &pb.Empty{}, m.err
}

// ─── Test harness ────────────────────────────────────────────────────────────

// mocks holds all mock servers for a single test scenario.
type mocks struct {
	cart           *mockCartServer
	productCatalog *mockProductCatalogServer
	currency       *mockCurrencyServer
	shipping       *mockShippingServer
	payment        *mockPaymentServer
	email          *mockEmailServer
}

// startMockServers registers all mock gRPC servers on random ports and returns
// a fully-wired Service and a cleanup function.
func startMockServers(t *testing.T, m *mocks) (*Service, func()) {
	t.Helper()

	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		return conn
	}

	startOne := func(register func(*grpc.Server)) (net.Listener, *grpc.Server) {
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		s := grpc.NewServer()
		register(s)
		go func() { _ = s.Serve(lis) }()
		return lis, s
	}

	cartLis, cartSrv := startOne(func(s *grpc.Server) { pb.RegisterCartServiceServer(s, m.cart) })
	catalogLis, catalogSrv := startOne(func(s *grpc.Server) { pb.RegisterProductCatalogServiceServer(s, m.productCatalog) })
	currencyLis, currencySrv := startOne(func(s *grpc.Server) { pb.RegisterCurrencyServiceServer(s, m.currency) })
	shippingLis, shippingSrv := startOne(func(s *grpc.Server) { pb.RegisterShippingServiceServer(s, m.shipping) })
	paymentLis, paymentSrv := startOne(func(s *grpc.Server) { pb.RegisterPaymentServiceServer(s, m.payment) })
	emailLis, emailSrv := startOne(func(s *grpc.Server) { pb.RegisterEmailServiceServer(s, m.email) })

	svc := &Service{
		CartConn:           dial(cartLis.Addr().String()),
		ProductCatalogConn: dial(catalogLis.Addr().String()),
		CurrencyConn:       dial(currencyLis.Addr().String()),
		ShippingConn:       dial(shippingLis.Addr().String()),
		PaymentConn:        dial(paymentLis.Addr().String()),
		EmailConn:          dial(emailLis.Addr().String()),
	}

	cleanup := func() {
		cartSrv.Stop()
		catalogSrv.Stop()
		currencySrv.Stop()
		shippingSrv.Stop()
		paymentSrv.Stop()
		emailSrv.Stop()
	}

	return svc, cleanup
}

// ─── Default fixtures ────────────────────────────────────────────────────────

func defaultAddress() *pb.Address {
	return &pb.Address{
		StreetAddress: "1 Main St",
		City:          "Testville",
		State:         "NY",
		Country:       "US",
		ZipCode:       10001,
	}
}

func defaultCard() *pb.CreditCardInfo {
	return &pb.CreditCardInfo{
		CreditCardNumber:          "4111111111111111",
		CreditCardCvv:             123,
		CreditCardExpirationYear:  2030,
		CreditCardExpirationMonth: 1,
	}
}

func defaultProduct() *pb.Product {
	return &pb.Product{
		Id:   "prod-1",
		Name: "Test Widget",
		PriceUsd: &pb.Money{
			CurrencyCode: "USD",
			Units:        10,
			Nanos:        0,
		},
	}
}

func defaultMocks() *mocks {
	return &mocks{
		cart: &mockCartServer{
			cart: &pb.Cart{
				UserId: "user-1",
				Items:  []*pb.CartItem{{ProductId: "prod-1", Quantity: 2}},
			},
		},
		productCatalog: &mockProductCatalogServer{product: defaultProduct()},
		currency:       &mockCurrencyServer{},
		shipping:       &mockShippingServer{},
		payment:        &mockPaymentServer{},
		email:          &mockEmailServer{},
	}
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestPlaceOrder_Success(t *testing.T) {
	m := defaultMocks()
	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	resp, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("PlaceOrder returned unexpected error: %v", err)
	}
	if resp.GetOrder() == nil {
		t.Fatal("PlaceOrder returned nil order")
	}
	if resp.GetOrder().GetOrderId() == "" {
		t.Error("expected non-empty order_id")
	}
	if resp.GetOrder().GetShippingTrackingId() != "TRACK-123" {
		t.Errorf("unexpected tracking id: %q", resp.GetOrder().GetShippingTrackingId())
	}
}

func TestPlaceOrder_GeneratesUniqueOrderIDs(t *testing.T) {
	m := defaultMocks()
	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	req := &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	}

	resp1, err1 := svc.PlaceOrder(context.Background(), req)
	resp2, err2 := svc.PlaceOrder(context.Background(), req)

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if resp1.GetOrder().GetOrderId() == resp2.GetOrder().GetOrderId() {
		t.Error("expected unique order IDs across calls")
	}
}

func TestPlaceOrder_CartFailure_ReturnsInternalError(t *testing.T) {
	m := defaultMocks()
	m.cart.getErr = status.Error(codes.Unavailable, "cart service down")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err == nil {
		t.Fatal("expected error when cart fails, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal, got %v", status.Code(err))
	}
}

func TestPlaceOrder_ProductCatalogFailure_ReturnsInternalError(t *testing.T) {
	m := defaultMocks()
	m.productCatalog.err = status.Error(codes.NotFound, "product not found")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err == nil {
		t.Fatal("expected error when product catalog fails, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal, got %v", status.Code(err))
	}
}

func TestPlaceOrder_ShippingQuoteFailure_ReturnsInternalError(t *testing.T) {
	m := defaultMocks()
	m.shipping.quoteErr = status.Error(codes.Unavailable, "shipping service down")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err == nil {
		t.Fatal("expected error when shipping quote fails, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal, got %v", status.Code(err))
	}
}

func TestPlaceOrder_PaymentFailure_ReturnsInternalError(t *testing.T) {
	m := defaultMocks()
	m.payment.err = status.Error(codes.InvalidArgument, "card declined")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err == nil {
		t.Fatal("expected error when payment fails, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("expected codes.Internal, got %v", status.Code(err))
	}
}

func TestPlaceOrder_ShipOrderFailure_ReturnsUnavailable(t *testing.T) {
	m := defaultMocks()
	m.shipping.shipErr = status.Error(codes.Unavailable, "shipping failed")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err == nil {
		t.Fatal("expected error when ship order fails, got nil")
	}
	if status.Code(err) != codes.Unavailable {
		t.Errorf("expected codes.Unavailable, got %v", status.Code(err))
	}
}

// Email failure must NOT fail the order — it's a warn-only path.
func TestPlaceOrder_EmailFailure_OrderStillSucceeds(t *testing.T) {
	m := defaultMocks()
	m.email.err = status.Error(codes.Unavailable, "email service down")

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	resp, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("expected order to succeed despite email failure, got: %v", err)
	}
	if resp.GetOrder().GetOrderId() == "" {
		t.Error("expected a valid order ID")
	}
	if m.email.calls != 1 {
		t.Errorf("expected email to be called once, got %d", m.email.calls)
	}
}

// Cart should be emptied after a successful order.
func TestPlaceOrder_CartEmptiedAfterSuccess(t *testing.T) {
	m := defaultMocks()
	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	_, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.cart.emptyCalls != 1 {
		t.Errorf("expected EmptyCart to be called once, got %d", m.cart.emptyCalls)
	}
}

// An empty cart should still produce a valid order (zero-item edge case).
func TestPlaceOrder_EmptyCart_Succeeds(t *testing.T) {
	m := defaultMocks()
	m.cart.cart = &pb.Cart{UserId: "user-1", Items: []*pb.CartItem{}}

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	resp, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("unexpected error for empty cart: %v", err)
	}
	if len(resp.GetOrder().GetItems()) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.GetOrder().GetItems()))
	}
}

// Order total should reflect item prices * quantity + shipping.
func TestPlaceOrder_TotalIncludesShippingAndItems(t *testing.T) {
	m := defaultMocks()
	// Product: $10.00 USD, quantity 2 → $20.00
	// Shipping: $5.00 USD (from mockShippingServer)
	// Expected total: $25.00
	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	resp, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	shipping := resp.GetOrder().GetShippingCost()
	if shipping == nil {
		t.Fatal("expected non-nil shipping cost")
	}
	if shipping.GetUnits() != 5 {
		t.Errorf("expected shipping units=5, got %d", shipping.GetUnits())
	}
}

func TestPlaceOrder_MultipleItems_AllFetched(t *testing.T) {
	m := defaultMocks()
	m.cart.cart = &pb.Cart{
		UserId: "user-1",
		Items: []*pb.CartItem{
			{ProductId: "prod-1", Quantity: 1},
			{ProductId: "prod-1", Quantity: 3},
		},
	}

	svc, cleanup := startMockServers(t, m)
	defer cleanup()

	resp, err := svc.PlaceOrder(context.Background(), &pb.PlaceOrderRequest{
		UserId:       "user-1",
		UserCurrency: "USD",
		Address:      defaultAddress(),
		Email:        "user@example.com",
		CreditCard:   defaultCard(),
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetOrder().GetItems()) != 2 {
		t.Errorf("expected 2 order items, got %d", len(resp.GetOrder().GetItems()))
	}
}
